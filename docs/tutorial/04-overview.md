# Overview

[English](04-overview.md) | [简体中文](04-overview.zh-CN.md)

## What it does

The Overview is the menu's first module and a read-only page of the running state, at
`#/gw.overview/overview`; it is the page a login opens. Its data comes from the Admin API's GET
endpoints, called through the console as `/api/m/admin/status`, `/api/m/admin/config/effective` and
`/api/m/admin/versions`: what fit runs now, where each part came from, and what has been going wrong.
Most parts link to the page that edits them. The page edits no config.

It refreshes every 5 seconds; the top bar's **Auto refresh** checkbox stops that (until it is ticked
again, for the rest of the visit to the workbench), its **Refresh** button reads again, and
`Updated <time>` says when the last read ended. A visit compares its own refreshes only.

## Pages

### Overview (`#/gw.overview/overview`)

**Six tiles**:

| Tile | Shows |
| --- | --- |
| Config version | The running config's version, its source (`file`, `store`, `console`, `admin`, `rollback` and so on) and how long ago it was applied |
| Reloads | The succeeded reloads since the start; `{n} failed` beside them, warned when any failed |
| Open connections | The connections open on the listeners' engine, the console's own included; `{n} refused` beside them |
| Routes | The route count of the effective config; `{n} layer 4 services` beside them |
| Backends up | `{up} / {all} up` over every upstream's backends, and the upstream count; warned when any is down |
| Certificates | The count of the certificates the listeners serve; how many expire within 14 days (or have expired), warned when any |

**Cards**:

- **Listeners** (with **Edit**): name, address, protocols.
- **Sources**: one row per source in precedence order — the source, and the listeners, routes,
  upstreams and layer 4 services it contributed.
- **Shadowed**, only when a provider provided an element the config leaves out: its source, the
  element's path in the provider's part, its name, and the source that kept it.
- **Upstream health** (with a filter and **Edit**, to the Upstreams page): one block per upstream —
  its name, `{up} / {all} up`, the requests in flight, its load-balancing strategy when one is set
  and its source, and an **Ask AI** button when a backend is down; below it each backend's address,
  state (up, down (health check), down (failures)), in flight, succeeded, failed. A backend counts as
  up until a health check or its failures mark it down, so one no request has tried yet shows up.
- **Routes** (with a filter and **Edit**, to the Routes page): name and priority, listeners (`all`
  when it names none), match (`everything` when it has none), middleware, handler (and its
  upstream), source. The filters match a row's text and keep across refreshes.
- **Layer 4 services** (with **Edit**): name, listeners, upstream, server names, PROXY protocol,
  source.
- **Certificates** (with **Certificates**): the same table as the [TLS](18-tls.md) page — the
  listener, the names, the issuer, the validity and the serial, with the days left or expired.
- **Recent versions** (with **All versions**, to the Versions page): the last 5 of the versions kept
  (`admin.history`, 10 by default), newest first, each with its number (the running one badged
  `running`), the time it applied and its source.

**Spikes**: an error counter that rose between two refreshes — requests failed on an upstream,
connections refused, failed reloads — shows as a warning row above the tiles saying how much rose in
how many seconds, and stays for a minute. Each row, and an upstream with backends down, has an **Ask AI**
button that asks the skill `diagnose` about it; for an upstream, the routes that proxy to it are the
skill's input `route`. The buttons are there only when the assistant is available. See
[Assistant](14-assistant.md).

### Reading it

- **After a save**: the Config version tile shows the new number and source, and Recent versions
  lists it as `running`; a config that failed to apply, from any source, counts in `{n} failed` on
Reloads and shows as a spike.
- **A backend down**: Backends up turns to a warning; the upstream's block says which backend and
  why (`down (health check)` or `down (failures)`), and its **Edit** opens the Upstreams page.
- **An element missing**: a route or upstream a provider sent that is not in the tables may be under
  **Shadowed**, its name taken by a source earlier in the precedence.

## See also

- [The console and the store](../console.md#the-pages) — where the Overview sits in the menu
- [The config reference: The config](../reference/config.md#the-config) — the fields of each part
- [Versions](09-versions.md), [TLS](18-tls.md), [Reverse proxy](15-reverse-proxy.md) and
  [L4 proxy](17-l4-proxy.md) — the pages its cards and buttons lead to
- [Consistency check](10-consistency-check.md) — what fit runs against what the effective config says
