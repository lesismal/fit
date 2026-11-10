# Engine

[English](06-engine.md) | [简体中文](06-engine.zh-CN.md)

## What it does

The Engine menu is the per-port view of the listeners: one first-level entry, **Engine** (tag
**Ports**), right after **Listeners** in the navigation bar, whose submenus are the listener ports,
each listing the features that port serves and the page that edits each one. The entry has no page
of its own — it expands into its ports, and every entry under a port is a link into a page of
another module.

Its data comes from the config draft the editing pages share, as last read, plus `GET /api/status`
for the addresses the kernel actually bound and for the `https` and `http` listeners HTTPS by
default makes. Each time the draft is read again (after a save, a discard, an import or a rollback)
the menu is rebuilt, keeping the page shown. Each port, and each entry that lists routes, has an
**Ask AI** button that asks the skill `diagnose` about it, its routes' names as the skill's input
`route`. See [Assistant](14-assistant.md).

This menu is not the **Engine** page of [Global settings](07-global-settings.md#engine-engine),
which edits the `engine` member: the event loops and workers of fib, the network engine.

## Pages

### Engine

**The ports**: the configured listeners in the order the config writes them; when none is written
and HTTPS by default is on, the `https` and `http` ports it generates (only `https` when its
redirects are turned off), at the ports `auto_https` names (443 and 80 by default); when there is no
port at all — or the config cannot be read — one **No listener** link to the listener form.

A port's row shows its name and a short address (`127.0.0.1:8080` reads as `:8080`). Its tooltip
holds the whole address, the tags, the address the kernel actually bound (when it differs from the
written one), and, when `/api/status` could not be read, a note that the addresses shown are the
config's.

The small line under a port's name is its protocols and the features with no page of their own, in
this order: the protocols (`HTTP/1.1`, `h2c`, `HTTP/2`, `TCP`, `UDP`, `SOCKS`; a listener that names
none shows its defaults, `HTTP/1.1`, and `HTTP/2` with `tls`), **Auto HTTPS** on a generated port,
**PROXY** (`proxy_protocol`), **HTTP/3** (`h3`), **On-demand** (`tls.on_demand`), **Client certs**
(`tls.client_auth`), **MPTCP** and **VOD** (the port serves `vod` routes).

**Each port's entries**, in a fixed order; only the features the port really has are shown. The
routes counted are those that name the port in `listeners`, or name no listener; a port that serves
no HTTP (`http/1.1`, `h2c`, `h2` or `h3`) has none.

1. The handlers of its routes, in the fixed order **Static files**, **Respond**, **FastCGI**,
   **Media tokens**, **Media pages**, then any other handler by its module id — for example
   `bt_publish`, `bt_stream` and `bt_tracker` from [BitTorrent](27-bittorrent.md), or `sync_ingest`
   from [Sync](26-sync.md);
2. **Reverse proxy routes** (handler `proxy`) and **Forward proxy routes** (handler
   `forward_proxy`);
3. **Cache zones** and **Cached routes**, when a route of the port uses the `cache` middleware;
4. **Other middleware**: the routes that use any other middleware, counted together; its tooltip
   lists the middleware modules;
5. **Streaming config**, when the port has streaming or `vod` routes, then each streaming handler's
   routes (**HLS routes**, **HTTP-FLV routes**, **WHIP publish routes**, **WHEP play routes**), then
   **VOD routes** and **Channels** — the number of `streaming.channels`, named in its tooltip, shown
   when the port has HLS, HTTP-FLV or WHEP play routes;
6. **WebRTC config** and each handler's routes (**WHIP routes**, **WHEP routes**, **WebRTC proxy
   routes**);
7. **Layer 4 services** (the services that list the port), **SOCKS forward proxy** (its rules are
   edited on the [Listeners](05-listeners.md) page), **TLS certificates** and, when `tls.acme` names
   hosts, **ACME certificates**; on a port that serves HTTP with an empty `tls` (as `tls {}`), or
   on the generated `https` port, while `auto_https` is not disabled, **Automatic HTTPS**; and on the generated `http` port **Redirects
   to HTTPS**;
8. **Port config**, the listener form, last.

Where each entry goes:

| entry | opens |
| --- | --- |
| a handler's routes, **Reverse proxy routes**, a streaming or WebRTC handler's routes | the Routes page of [Reverse proxy](15-reverse-proxy.md), filtered to that handler (`#/gw.reverse/routes?handler=<module>`), with the count |
| **Forward proxy routes** | the [Forward proxy](16-forward-proxy.md) page, with the count |
| **Cached routes**, **Other middleware** | the Routes page, unfiltered, with the count |
| **Cache zones** | the [Cache](19-cache.md) zones page |
| **Streaming config**, **WebRTC config** | the [Streaming](20-streaming.md) and [WebRTC](23-webrtc.md) config pages |
| **VOD routes**, **Channels** | the VOD routes and Channels pages of the [Media library](22-media-library.md), with the count |
| **Layer 4 services** | the [L4 proxy](17-l4-proxy.md) services page, with the count |
| **TLS certificates**, **ACME certificates**, **Automatic HTTPS**, **Redirects to HTTPS** | the Certificates, ACME and Automatic HTTPS pages of [TLS](18-tls.md) |
| **SOCKS forward proxy**, **Port config** | the [Listeners](05-listeners.md) page |

**Automatic HTTPS** on a port is a link to the `auto_https` settings. It is listed only where HTTPS by
default acts: a listener whose `tls` is empty, or the ports it generates when no listener is written. A
listener written without `tls` stays plain HTTP and lists neither.

## See also

- [The console and the store](../console.md#the-pages) — the Engine menu described
- [Listeners](05-listeners.md) — editing the listeners themselves
- [The config reference: Listeners](../reference/config.md#listeners) — every listener field, and
  [HTTPS by default](../reference/config.md#https-by-default)
- [Reverse proxy](15-reverse-proxy.md), [L4 proxy](17-l4-proxy.md), [Streaming](20-streaming.md) and
  [WebRTC](23-webrtc.md) — the pages a port's entries link to
- [Sync](26-sync.md) and [BitTorrent](27-bittorrent.md) — the modules whose handlers are listed by
  module id
