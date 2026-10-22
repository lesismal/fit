# 009: How providers' config is merged

[English](009-provider-merging.md) | [简体中文](009-provider-merging.zh-CN.md)

Status: accepted, in I56 (2026-10-22).

## Context

From I57 on, providers (Docker, Kubernetes, Consul, etcd) add routes and upstreams to what the
config file and the Admin API write. The roadmap asks for merging rules that are written down and
visible: which source wins, and an Admin API endpoint that shows the effective config and where each
part came from. I56 has to settle what a provider may set, what happens when two sources use one
name, how errors are reported, and what the Admin API's existing endpoints mean once config comes
from more than one place.

## Decision

- **The base config and parts.** The base config is the one the file, SIGHUP and the Admin API
  write; `GET /config`, its JSON pointers, ETags, versions and rollback are all about it, as
  before. A provider sends a part: a JSON object with `listeners`, `routes`, `upstreams` and
  `services`, and nothing else. Every other setting (`access_log`, `limits`, `admin`, `acme`,
  `auto_https`, `metrics`) comes from the base config alone, so a container label cannot turn off
  the Admin API's token or change where certificates are kept.
- **Precedence.** The base config wins over every provider, and a provider over those after it,
  in the order they are added (from I57, the order the config lists them). A part's elements are
  added after the base config's, provider by provider in that order, each part's in its own order;
  the base config's elements keep their indexes, so its error paths and JSON pointers mean the same
  with or without providers.
- **Names.** A named element (a listener, upstream or service, or a route with a name) whose name a
  source before it already uses is left out, and listed as shadowed, with who took the name.
  Unnamed routes never collide. Two elements of one name from the same source are not resolved:
  the config's own check refuses them, as it would in a file. Names are not namespaced by provider
  (Traefik's `name@docker`): that would mean rewriting the references to them inside module
  params, which fit does not interpret.
- **All or nothing.** The merged config is checked, compiled and applied as a whole, through the
  path every config takes (`admin.Configs`, one at a time). A part that would leave it invalid is
  refused, and the provider's part before it stays; so is a base config that would break what a
  provider provides, such as one removing a listener a provider's route names.
- **Errors where they were written.** The merged config is the base config's text with the parts'
  elements spliced in as written, so each error is mapped back: one in the base config gets its
  line and column there, one in a provider's element gets its path and position in that
  provider's part, and says `(provided by <name>)`.
- **Visible.** `GET /config/effective` answers the running config as it runs, the version and the
  base config's ETag, the sources in order of precedence, where each top-level member and each
  array element came from (a JSON pointer in the merged config, its source, and a pointer into
  the provider's part), and what was shadowed. The base config's source is the last thing that
  set it whole: `file` or `admin`, or, after a rollback, the source of the version rolled back to.
- **Bounded.** A part is at most 16 MiB. `provider.Run` keeps at most one part waiting per
  provider: one sent while another waits replaces it, so a provider that changes faster than
  configs apply skips what is out of date. A part the same as the provider's last is not applied
  again. Providers and merging run on goroutines of their own in the control plane.

## Consequences

- A provider cannot stop serving something the base config names, nor take over a name the
  operator wrote; to let a provider own a name, leave it out of the base config.
- A provider whose part is refused keeps serving its previous part until it sends one that loads;
  the error is reported where the provider is run. A provider that has gone sends an empty part.
- Each provider's update is a new config version, with the provider as its source; `GET /versions`
  shows them, and a rollback restores the base config with what providers provide now.
