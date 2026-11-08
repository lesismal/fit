# Engine

[English](06-engine.md) | [简体中文](06-engine.zh-CN.md)

## What it does

The Engine menu is the per-port view of the running listeners: one first-level entry, "Engine"
(tag "Ports"), whose submenus are the listener ports, each listing the features that port serves
and the page that edits each one. The module has no page of its own — every entry is a link into
a page of another module.

Its data comes from the config draft the editing pages share, plus `GET /api/status` for the
addresses the kernel bound and the listeners HTTPS by default makes; a config change or a refresh
rebuilds the menu without losing the page shown. Each port, and each entry that lists routes, has
an **Ask AI** button that asks the skill `diagnose` about it. See [Assistant](14-assistant.md).

## Pages

### Engine (`#/engine`)

**The ports**: in the order the config writes them; when none are written and HTTPS by default is
on, the `https` and `http` ports it generates; when there is no port at all, one link to the
listener form. A port's row shows its name and a short address (`127.0.0.1:8080` reads as
`:8080`); the whole address, the address the kernel actually bound (when it differs from the
written one) and a failed status read are in the entry's title.

The small line under a port's name is its protocols and the features with no page of their own:
the protocols (HTTP/1.1, h2c, HTTP/2, TCP, UDP, SOCKS), the PROXY protocol, HTTP/3, the
on-demand certificates, the client certificates, automatic HTTPS, MPTCP and VOD.

**Each port's entries** (in a fixed order; only the features the port really has are shown):

1. The handlers of its routes: static files, respond, FastCGI, media tokens, media pages, then
   any other handler by its module id;
2. The reverse and forward proxy routes;
3. The cache zones and the cached routes (when a route uses the `cache` middleware);
4. The other middleware, grouped (its title lists the modules);
5. The streaming config, each streaming handler's routes (HLS, HTTP-FLV, WHIP publish, WHEP
   play), the VOD routes, and the channels its egress plays;
6. The WebRTC config and each handler's routes (WHIP, WHEP, WebRTC proxy);
7. The layer 4 services, the SOCKS forward proxy (its rules are edited on the
   [Listeners](05-listeners.md) page), the TLS certificates, the ACME certificates, automatic
   HTTPS and its redirect;
8. The listener form, last.

The entries that list routes carry a count; clicking one opens the routes page filtered to that
handler.

## See also

- [The console and the store](../console.md#the-pages) — the Engine menu described
- [Listeners](05-listeners.md) — editing the listeners themselves
- [The config reference: Listeners](../reference/config.md#listeners) — every listener field
