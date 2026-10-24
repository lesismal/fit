# 021: fit's own .konf format, and configs in several files

[English](021-fit-format-and-config-files.md) | [简体中文](021-fit-format-and-config-files.zh-CN.md)

Status: accepted, in I80 (2026-10-23). Replaces [decision 020](020-caddyfile-and-jsonc.md)'s first
point, Caddy's Caddyfile as fit's block syntax.

## Context

Decision 020 gave fit a block syntax by reading Caddy's own Caddyfile. A Caddyfile says what Caddy
can say, in Caddy's directives: much of fit has no directive in it (layer 4 services, caching, rate
limits, JWT, WASM, providers, telemetry, fit's limits), and what it has is converted with Caddy's
semantics, a chain of handlers, into fit's routes, with doubts listed. Users asked for a format
with the shape of a Caddyfile or an nginx config whose content is fit's own: every field JSON and
YAML have, no more and no less, with configs from other servers a matter for the docs and for
conversion, not for fit's format. They asked too that a config may be split across files and
directories, as nginx's `include` and Caddy's `import` split theirs.

## Decision

- **A format of fit's own, `.konf`, derived from the config's types.** `adapter/fitconf` reads
  blocks of directives, each a field: a scalar takes one argument, a list its arguments, an object
  or a map a block, a list of objects a repeated block, and a module its name and a block of its
  params. Which fields there are and their types come from the Go types the config decoder reads
  and from each module's params, so a field or module added to fit is written in `.konf` with no
  change to the adapter, and the format cannot drift from JSON and YAML. Values are typed by their
  field, and one of the wrong type is kept for the config's check, which reports it where it was
  written.
- **Sites, and a few shorthands.** `site <hosts...> { route <paths...> { ... } }` writes the
  routes of a set of host names together, with what they share (listeners, priority, match,
  middleware). It expands to ordinary routes before the conversion, so it adds no semantics of its
  own; a route can always be written out instead. The top-level lists take their names in the
  singular with the element's name as the argument (`listener web { ... }`), so that a config reads
  as nginx's and Caddy's do. Later (2026-10-24), to be as short as a Caddyfile, a few more that
  expand the same way, before the conversion and with no semantics of their own: a listener's
  address and protocols and an upstream's addresses as arguments after the name, `tls <cert>
  <key>`, `site *` for any host, numbers of bytes in units (`1GiB`), and snippets,
  `snippet <name> { ... }` read in place by `include <name>`, beside `include` and `import` of
  files. `FromJSON` writes the shorthands, and blocks that fit on a line on one.
- **Writing back.** `fitconf.FromJSON` writes native JSON as a `.konf` file that adapts back to the
  same config, for `fit adapt -to fit`, for the `.konf` template and for the config reference's
  examples, which are made from their JSON, so that they cannot disagree.
- **Caddy and nginx configs are converted, not read as fit's.** `adapter/caddyfile` and
  `adapter/nginx` stay, as migration tools: `fit adapt` converts them, best to a `.konf` file, and
  `fit run` still takes one named with `-c`. The config docs and templates are about fit's three
  formats; Caddy and nginx appear in the migration guides and the page on converting a Caddyfile.
- **Several files and directories.** `-c` is repeated and may name a directory, which stands for
  its `.konf`, `.yaml`, `.yml` and `.json` files, recursively, in lexical order, hidden ones left
  out. Each file is adapted on its own and the results merged as JSON: the top-level lists are
  joined in order, and any other field is given by one file only (`schema_version` by several if
  they agree), so that a merge never has to decide which of two values wins. Errors in the merged
  config are mapped back to the file, its path there and the place. The file watcher watches the
  set, looking for files added and taken out, and every file is read again on each change, within
  bounds (10,000 files, 64 MiB).
- **No new dependency.** The lexer and parser are fit's own, bounded in words and nesting.

## Consequences

- `fit run`, `fit validate` and `fit adapt` take `.konf` files by their extension, and several
  `-c`; `fit adapt -to fit` writes one; `fit schema -template fit` prints the template.
- `templates/caddyfile` is gone; `templates/konf/fit.konf` is generated with the YAML and JSON
  templates. `examples/*.caddyfile` became `examples/*.konf`.
- A config's error names its file, through `config.Error`'s `File`.
- The Admin API still takes and returns one JSON config; what `-c` read is merged before it.

## Later: the extension .konf

On 2026-10-24 the format's extension became `.conf`, chosen for what the files are, blocks of
directives like nginx's and Caddy's, rather than for fit's name; `.fit` also named Garmin's
activity files and FITS images. The same day it became `.konf`, so that fit's files are not
mistaken for nginx's or for the many other `.conf` files. Neither `.fit` nor `.conf` is read, the
project not having been released. `fit run` and `fit validate` load only fit's own formats
(`.konf`, YAML, JSON): the nginx and Caddyfile adapters take no extension or file name, and
`fit adapt -from nginx` or `-from caddyfile` converts those configs into one of fit's. The
format is still called fit's format, `-to fit` still writes it, and its package is still
`adapter/fitconf`. The sections above have since been written with the current names.
