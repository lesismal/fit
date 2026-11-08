# Overview

[English](04-overview.md) | [简体中文](04-overview.zh-CN.md)

## What it does

The Overview is a read-only page of the running state, from the Admin API's GET endpoints
(`/status`, `/config/effective`, `/versions`). It refreshes every 5 seconds (the top bar can turn
auto refresh off, or refresh by hand), and links to the pages that edit each part. The page edits
no config.

## Pages

### Overview (`#/gw.overview/overview`)

**Six tiles**:

| Tile | Shows |
| --- | --- |
| Config version | The running config's version, its source and how long ago it was applied |
| Reloads | The succeeded reloads, the failed ones (warned when any failed) |
| Open connections | The open connections, the refused ones |
| Routes | The route count, the layer 4 service count |
| Backends up | Up / all backends, the upstream count (warned when any is down) |
| Certificates | The certificate count, how many expire within 14 days (warned when any) |

**Cards** (each with a link to the page that edits it):

- **Listeners**: name, address, protocols.
- **Sources**: the listeners, routes, upstreams and services each source (a file, a provider)
  contributed, in the precedence order.
- **Shadowed**: elements a provider provided but the config left out, because a source before
  them took their names.
- **Upstream health**: one block per upstream — its name, up / all backends, the requests in
  flight, the load-balancing strategy, its source; below it each backend's address, state (up,
  down (health check), down (failures)), in flight, succeeded, failed.
- **Routes**: name and priority, listeners, match, middleware, handler (and its upstream), source.
- **Layer 4 services**: name, listeners, upstream, server names, PROXY protocol, source.
- **Certificates**: the same certificate table as the [TLS](18-tls.md) page.
- **Recent versions**: the last 5 versions, with a link to the [Versions](09-versions.md) page for
  all of them.

The routes and upstreams cards each have a filter; the **Edit** buttons go to the
[Reverse proxy](15-reverse-proxy.md) and [L4 proxy](17-l4-proxy.md) pages.

**Spikes**: the error counters that rose between two refreshes — requests failed on an upstream,
connections refused, reloads failed — show as warning rows and stay listed for a minute. An
upstream with backends down and each spike have an **Ask AI** button that asks the skill
`diagnose` about it. See [Assistant](14-assistant.md).

## See also

- [The console and the store](../console.md#the-pages) — where the Overview sits in the menu
- [The config reference: The config](../reference/config.md#the-config) — the fields of each part
