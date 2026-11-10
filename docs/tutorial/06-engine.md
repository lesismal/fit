# Engine

[English](06-engine.md) | [简体中文](06-engine.zh-CN.md)

## What it does

The Engine menu is the per-port view of the running listeners: one first-level entry, **Engine**
(tag **Ports**), whose submenus are the listener ports, each listing the features that port serves
and the page that edits each one. The entry has no page of its own — it expands into its ports, and
every entry under a port is a link into a page of another module.

Its data comes from the config draft the editing pages share, plus `GET /api/status` for the
addresses the kernel actually bound and for the `https` and `http` listeners HTTPS by default makes.
A config change (the shared draft's `fit:config` event) or a refresh rebuilds the menu, keeping the
page shown. Each port, and each entry that lists routes, has an **Ask AI** button that asks the
skill `diagnose` about it, its routes' names as the skill's input `route`. See
[Assistant](14-assistant.md).

## Pages

### Engine

**The ports**: the configured listeners in the order the config writes them; when none is written
and HTTPS by default is on, the `https` and `http` ports it generates (only `https` when its
redirects are turned off); when there is no port at all — or the config cannot be read — one
**No listener** link to the listener form. A port's row shows its name and a short address
(`127.0.0.1:8080` reads as `:8080`); the whole address, the tags, the address the kernel actually
bound (when it differs from the written one) and a failed status read are in the entry's title.

The small line under a port's name is its protocols and the features with no page of their own, in
this order: the protocols (`HTTP/1.1`, `h2c`, `HTTP/2`, `TCP`, `UDP`, `SOCKS`; HTTP/3 is the tag
below), **Auto HTTPS** on a generated port, **PROXY**, **HTTP/3**, **On-demand**, **Client certs**,
**MPTCP** and **VOD**.

**Each port's entries** (in a fixed order; only the features the port really has are shown):

1. The handlers of its routes, in the fixed order **Static files**, **Respond**, **FastCGI**,
   **Media tokens**, **Media pages**, then any other handler by its module id — for example
   `bt_publish`, `bt_stream` and `bt_tracker` from [BitTorrent](27-bittorrent.md), or `sync_ingest`
   from [Sync](26-sync.md);
2. **Reverse proxy routes** and **Forward proxy routes**;
3. **Cache zones** and **Cached routes**, when a route of the port uses the `cache` middleware;
4. **Other middleware**, grouped; its title lists the middleware modules the port's routes use;
5. **Streaming config**, then each streaming handler's routes (**HLS routes**, **HTTP-FLV routes**,
   **WHIP publish routes**, **WHEP play routes**), then **VOD routes** and **Channels** — the
   channels whose egress the port plays, when it has HLS, HTTP-FLV or WHEP routes and there are
   channels;
6. **WebRTC config** and each handler's routes (**WHIP routes**, **WHEP routes**, **WebRTC proxy
   routes**);
7. **Layer 4 services**, **SOCKS forward proxy** (its rules are edited on the
   [Listeners](05-listeners.md) page), **TLS certificates**, **ACME certificates**, **Automatic
   HTTPS** (when the port serves HTTP, has no `tls` and HTTPS by default is on), and on the
   generated `http` port **Redirects to HTTPS**;
8. **Port config**, the listener form, last.

The entries that list a handler's routes carry the count and open the routes page filtered to that handler
(the forward proxy has a page of its own for its routes); the others open their own page.

## See also

- [The console and the store](../console.md#the-pages) — the Engine menu described
- [Listeners](05-listeners.md) — editing the listeners themselves
- [The config reference: Listeners](../reference/config.md#listeners) — every listener field
- [Reverse proxy](15-reverse-proxy.md), [L4 proxy](17-l4-proxy.md), [Streaming](20-streaming.md) and
  [WebRTC](23-webrtc.md) — the pages a port's entries link to
- [Sync](26-sync.md) and [BitTorrent](27-bittorrent.md) — the modules whose handlers are listed by
  module id
