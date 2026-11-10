# Overview

[English](04-overview.md) | [简体中文](04-overview.zh-CN.md)

## What it does

The Overview is the menu's first module and a read-only page of the running state, at
`#/gw.overview/overview`. Its data comes from the Admin API's GET endpoints (`/status`,
`/config/effective`, `/versions`): what fit runs now, where each part came from, and what has been
going wrong. Most parts link to the page that edits them. The page edits no config.

It refreshes every 5 seconds; the top bar's **Auto refresh** checkbox stops that, and its
**Refresh** button reads again, saying `Updated <time>`. A visit compares its own refreshes only.

## Pages

### Overview (`#/gw.overview/overview`)

**Six tiles**:

| Tile | Shows |
| --- | --- |
| Config version | The running config's version, its source and how long ago it was applied |
| Reloads | The succeeded reloads; `{n} failed` beside them, warned when any failed |
| Open connections | The open connections; `{n} refused` beside them |
| Routes | The route count; `{n} layer 4 services` beside them |
| Backends up | `{up} / {all} up` over every upstream's backends, and the upstream count; warned when any is down |
| Certificates | The certificate count; how many expire within 14 days, warned when any |

**Cards**:

- **Listeners** (with **Edit**): name, address, protocols.
- **Sources**: one row per source in precedence order — the source, and the listeners, routes,
  upstreams and layer 4 services it contributed.
- **Shadowed**, only when a provider provided an element the config leaves out: its source, the
  element's path in the provider's part, its name, and the source that kept it.
- **Upstream health** (with a filter and **Edit**): one block per upstream — its name, `{up} / {all}
  up`, the requests in flight, its load-balancing strategy and its source, and an **Ask AI** button
  when a backend is down; below it each backend's address, state (up, down (health check), down
  (failures)), in flight, succeeded, failed.
- **Routes** (with a filter and **Edit**): name and priority, listeners, match, middleware, handler
  (and its upstream), source.
- **Layer 4 services** (with **Edit**): name, listeners, upstream, server names, PROXY protocol,
  source.
- **Certificates** (with **Certificates**): the same table as the [TLS](18-tls.md) page — the
  listener, the names, the issuer, the validity and the serial, with the days left or expired.
- **Recent versions** (with **All versions**): the last 5 versions, newest first, each with its
  number (the running one badged), the time it applied and its source.

**Spikes**: an error counter that rose between two refreshes — requests failed on an upstream,
connections refused, failed reloads — shows as a warning row saying how much rose in how many
seconds, and stays for a minute. Each row, and an upstream with backends down, has an **Ask AI**
button that asks the skill `diagnose` about it; for an upstream, the routes that proxy to it are the
skill's input `route`. See [Assistant](14-assistant.md).

## See also

- [The console and the store](../console.md#the-pages) — where the Overview sits in the menu
- [The config reference: The config](../reference/config.md#the-config) — the fields of each part
- [Versions](09-versions.md), [TLS](18-tls.md), [Reverse proxy](15-reverse-proxy.md) and
  [L4 proxy](17-l4-proxy.md) — the pages its cards and buttons lead to
- [Consistency check](10-consistency-check.md) — what fit runs against what the effective config says
