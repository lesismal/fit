# JSON

[English](08-json.md) | [简体中文](08-json.zh-CN.md)

## What it does

The **JSON** entry of the menu (after **Global**) is one page, **Config as JSON**, at `#/sys.json/json`,
which shows and edits the whole config as one JSON object, on two tabs: **Draft** and **Effective**. The
draft is the one the forms are editing; the effective config is the one fit actually runs: the base
config with what the providers provide. The two differ routinely — the effective config has the
providers' elements, which the draft does not, and the draft may hold changes not saved yet; what fit
runs against what the effective config says is the [consistency check](10-consistency-check.md)'s
subject.

This is where the whole config is read and changed: a member with no page of its own (or a page that edits
only part of it) is edited here; a JSON file brought from elsewhere can be opened and applied here; the
draft can be downloaded from here. `admin` has no form on any page. When fit keeps its config in
`admin.store`, `admin` is not in the draft at all, since it comes only from the bootstrap file, and a
config sent with `admin` is refused. Without a store the draft is the config as its file writes it,
`admin` included when the file sets it, and a change to `admin` waits for a restart.

The page shares the same draft and the same save path as every other page that edits the config, so saving
here is saving from a form: the config is checked, then applied as a new config version, and written to
the store when there is one. Without a store a saved change lasts until fit restarts from its config
files.

## Pages

### Draft

One text box, the whole config as JSON; this page has only the two tabs, not the `.conf`, `.yaml` and
`.json` ones, nor the **Export** and **Import** buttons, of the pages that edit part of the config. Edits
here are the same draft the forms edit: a field changed in a form shows up here, and JSON typed here is
parsed into the forms' tree as you type. The text must be a JSON object. When it does not parse, it is
kept as typed, the error is shown ("Does not parse: …"), the forms still show the last JSON that parsed,
and saving is blocked on every page — the others say "The JSON page holds text that does not parse" —
until it is fixed or discarded.

**Save and apply** sends the whole draft (`PUT /config` with `If-Match`): the config is loaded and
compiled, then applied as a new config version; problems are listed with their paths, each a link to the
page that edits it. **Check** is the same request with `?dry_run=1`, which checks without applying and
says which listeners would be added or removed. When the config was changed elsewhere after it was read
(`412`), the page says so, with a **Discard and read again** button: copy the changes from here first if
you want to keep them. The save bar shows the config version, or the **Unsaved changes** badge, with
**Discard**, **Check** and **Save and apply**, all disabled while nothing is unsaved.

This page's tools:

| tool | what it does |
| --- | --- |
| **Format** | Rewrites the text as the draft's JSON, indented two spaces; while the text does not parse it does nothing |
| **Copy** | Copies the text |
| **Open file** | Reads a `.json` file into the text box, parsed as typed text is; nothing is saved until **Save and apply** |
| **Download** | Saves the text as it stands, unsaved edits included, as `fit-config-<version>.json`, the version the draft was read at |

To bring a `.conf` or `.yaml` file in, use **Import** on the [Global settings](07-global-settings.md)
chapter's Import / Export page instead: it converts and applies it at once.

### Effective

Read only: the base config with what the providers provide, which is what fit runs, read from
`GET /config/effective` each time the tab opens. Three blocks:

- **Precedence**: first to last, the base config's source first and then the providers in the order they
  are written. The earlier one wins.
- **Sources**: where each member and each array element of the effective config came from — the path, the
  source, and its place in what that provider provided (empty for the base config).
- **Effective · Version N**: the JSON, formatted, with a **Copy** button.

The base config's source is where its last change came from: `file` (the config files), `store`,
`admin` (the Admin API), `console` (a console page), or `cluster` (a manager of many fits). A rollback, or
a reload after a certificate file changed, keeps the source of the config it applies again; the
[Versions](09-versions.md) page shows `rollback` and `certificates` as the source of those versions.

The shadowed elements — what a provider provided but a source before it took the same name — are not in the
effective config; they appear as info on the Overview page and on the consistency check.

What the effective view shows, a small piece, with an `etcd` provider named `kv`:

```json
{
  "listeners": [
    { "name": "web", "address": ":8080", "protocols": ["http/1.1"] },
    { "name": "shop", "address": ":8081", "protocols": ["http/1.1"] }
  ],
  "routes": [
    {
      "name": "api",
      "match": { "host": ["api.example.com"] },
      "handler": { "module": "proxy", "params": { "upstream": "backend" } }
    }
  ],
  "upstreams": [
    { "name": "backend", "addresses": ["127.0.0.1:9000"] }
  ]
}
```

The precedence reads `console › kv`, and the sources table beside it:

| Path | Source | |
| --- | --- | --- |
| `/listeners/0` | `console` | |
| `/listeners/1` | `kv` | `/listeners/0` |
| `/routes/0` | `console` | |
| `/upstreams/0` | `kv` | `/upstreams/0` |

The `shop` listener and the `backend` upstream are not in the draft: the provider provided them. Writing a
listener named `shop` into the draft turns it from "provided by `kv`" into "written by the config", and the
provider's element of that name is then shadowed.

## Key parameters

This page edits the whole config, not one member, so it has no field table of its own; the members' tables
are in the matching chapters of the config reference:
[The config](../reference/config.md#the-config), [Listeners](../reference/config.md#listeners),
[Routes](../reference/config.md#routes), [Upstreams](../reference/config.md#upstreams),
[Services](../reference/config.md#services), [Providers](../reference/config.md#providers).

The precedence and the merge rules:

| | |
| --- | --- |
| The base config | wins over every provider |
| A provider | wins over the providers listed after it; an element whose name one before it already took is shadowed |
| The arrays | `listeners`, `routes`, `upstreams` and `services` are merged across the files and the providers — a provider provides only those four kinds — while `providers` itself is joined across the files; every other member is written in one place only |

The endpoints this page uses (on the console they are under `/api/m/admin/`, as `/api/m/admin/config`):

| endpoint | |
| --- | --- |
| `GET /config` | the draft, the base config as JSON, with its `ETag` and `Fit-Config-Version` |
| `GET /schema` | the JSON Schema the forms are made from |
| `PUT /config` | saves the whole config, with `If-Match`; with `?dry_run=1`, **Check** |
| `GET /config/effective` | the effective config, below |

What `GET /config/effective` answers:

| field | what it is |
| --- | --- |
| `version` | The running config's version, shown in the Effective block's heading |
| `etag` | The base config's ETag, not the merged one's |
| `precedence` | The order of the sources, the base config's first |
| `config` | The effective config as JSON |
| `sources` | Where each member and each array element came from: `path` (a JSON pointer into the merged config), `source`, and `from` (a JSON pointer into what the provider provided, left out for the base config) |
| `shadowed` | The elements left out: `source`, `from`, `name`, and `by`, the source whose element of that name was kept |

The page shows the precedence, the sources and the JSON; `shadowed` is shown on the Overview page and on the
consistency check.

## Common cases

- **Changing several members at once**: change them here, or on several pages, then save once; it makes one
  version.
- **A file brought from elsewhere**: after **Open file** the text has to parse before it can be saved; when it
  does not, the page shows the error and keeps the text as typed. **Check** before **Save and apply** shows
  what it would do.
- **Seeing what fit actually runs**: the Effective tab includes the elements the providers provided, which
  are not in the draft; what fit runs against that effective config is what the
  [consistency check](10-consistency-check.md) lists.
- **The config was changed elsewhere**: on `412`, copy the changes out first if you want them, then
  **Discard and read again**.

## In .conf

This is a JSON page, so there is no .conf example here; the .conf form and the three formats of a whole
config are in [the .conf format](../reference/fit.md) and [the formats](../configuration.md#the-formats).
The JSON above is what the page shows.

## See also

- [The config](../configuration.md): [the formats](../configuration.md#the-formats),
  [several files and directories](../configuration.md#several-files-and-directories),
  [changing a running config](../configuration.md#changing-a-running-config).
- [The config reference](../reference/config.md) and [the .conf format](../reference/fit.md); the
  [Admin API](../reference/config.md#admin-api) chapter has the endpoints' answers in full.
- [The console and the store](../console.md#the-pages): where this page sits in the menu, and the
  [store](../console.md#the-store) the draft is written to.
- The [Global settings](07-global-settings.md) chapter's Import / Export page: the whole config as `.conf`,
  `.yaml`, `.json` and `.store`.
- Neighbouring chapters: [the function navigation bar](03-navigation.md), [Versions](09-versions.md),
  [Consistency check](10-consistency-check.md).
