# JSON

[English](08-json.md) | [简体中文](08-json.zh-CN.md)

## What it does

The **JSON** page shows and edits the whole config as one JSON object, on two tabs: **Draft** and
**Effective**. The draft is the one the forms are editing; the effective config is the one fit actually runs:
the base config with what the providers provide. Where the two differ, the differences are listed on the
[consistency check](10-consistency-check.md) page.

This is where the whole config is read and changed: a member with no page of its own (or a page that edits
only part of it) is edited here; a JSON file brought from elsewhere can be opened and applied here; a config
handed to another machine is downloaded from here. It does not edit `admin`: when fit keeps its config in
`admin.store`, `admin` is not in the draft.

The page shares the same draft and the same save path as every other page that edits the config, so saving
here is saving from a form: the config is checked, then applied as a new config version and written to the
store.

## Pages

### Draft

One text box, the whole config as JSON. Edits here are the same draft the forms edit: a field changed in a
form shows up here, and JSON typed here is parsed into the forms' tree; when it does not parse the text is
kept as typed and blocks saving, with the error shown ("Does not parse: …"), and the forms still show the
last JSON that parsed. Leaving the tab parses once first; if it does not parse, the page stays on this tab.

**Save and apply** checks this JSON and applies it as one, through the same path as the other pages: the
config is loaded and compiled, then applied as a new config version and written to the store, and errors show
at their own paths, with the path, line and column. **Check** is the same request with `?dry_run=1`, which
checks without applying. When the config was changed elsewhere after it was read (`412`), the page says so:
discard the changes to read it again, or copy the changes from here first. The save bar shows the config version
and "Unsaved changes", with **Save and apply**, **Check** and **Discard**.

This page's tools: **Format** (rewrites the text from the draft's values), **Copy**, **Open file** (reads a
`.json` file in), and **Download** (saved as `fit-config-<version>.json`). Text that has not been edited
follows the draft; once you type in it, what you typed is what counts.

### Effective

Read only: the base config with what the providers provide, which is what fit runs. Three blocks:

- **Precedence**: first to last, the base config's source first and then the providers in the order they are
  written. The earlier one wins.
- **Sources**: where each member and each array element of the effective config came from — the path, the
  source (`file`, `admin`, `rollback` or a provider's name), and its place in what that provider provided (a
  row for the base config has no such column).
- **Effective · Version N**: the JSON, formatted, with a **Copy** button.

The shadowed elements — what a provider provided but a source before it took the same name — are not in the
effective config; they appear as info on the Overview page and on the consistency check.

What the effective view shows, a small piece:

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

The sources table beside it:

| Path | Source | |
| --- | --- | --- |
| `/listeners/0` | `file` | |
| `/listeners/1` | `docker` | `/listeners/0` |
| `/routes/0` | `file` | |
| `/upstreams/0` | `docker` | `/upstreams/0` |

The draft and the effective config often differ: the `shop` listener is not in the draft, the provider
provided it; writing it into the draft turns it from "managed by the provider" into "written by the config",
and the provider's element of that name is then skipped.

## Key parameters

This page edits the whole config, not one member, so it has no field table of its own; the members' tables
are in the matching chapters of the config reference:
[The config](../reference/config.md#the-config), [Listeners](../reference/config.md#listeners),
[Routes](../reference/config.md#routes), [Upstreams](../reference/config.md#upstreams),
[Services](../reference/config.md#services), [Providers](../reference/config.md#providers).

The precedence and the merge rules:

| | |
| --- | --- |
| The base config | wins over every provider; its source is where it was last changed: `file`, `admin` or `rollback` |
| A provider | wins over the providers listed after it; an element whose name one before it already took is skipped |
| The arrays | `listeners`, `routes`, `upstreams`, `services` and `providers` are merged across files and sources; every other member is written in one place only |

The endpoints this page uses (the Admin API's endpoints sit under `/api/m/admin/` on the console):

| endpoint | |
| --- | --- |
| `GET /config/effective` | the effective config: its version, ETag, precedence, sources and JSON |
| `POST /config/render?format=json` | renders the draft to text |
| `POST /config/parse?format=json` | parses the text back into the draft |
| `PUT /config` | saves the whole config, with `If-Match` |

The three columns of the sources table:

| column | what it is |
| --- | --- |
| Path | A JSON pointer to the element in the merged config |
| Source | The provider it came from, or the base config's source |
| | A JSON pointer to it in what that provider provided; a row for the base config has no such column |

The Effective block's heading carries the running version; its ETag is the base config's, not the merged one's.

What `GET /config/effective` answers:

| field | what it is |
| --- | --- |
| `version` | The running config's version |
| `etag` | The base config's ETag |
| `precedence` | The order of the sources, the base config first |
| `config` | The effective config as JSON |
| `sources` | Where each member and each array element came from |
| `shadowed` | The elements that were left out |

The page shows the precedence, the sources and the JSON; `shadowed` is shown on the Overview page and on the
consistency check.

## Common cases

- **Changing the whole config at once**: the draft is the one the forms edit, so saving here is saving from a
  form; changing several members here is faster than saving page by page and makes one version.
- **A file brought from elsewhere**: after **Open file** the text has to parse before it can be saved; when it
  does not, the page stays on the Draft tab and shows the error.
- **Seeing what fit actually runs**: the Effective tab reads `GET /config/effective`, which includes the
  elements the providers provided, and those are often not in the draft.
- **Without a store**: the console changes the running config in memory like the Admin API does, and starts
  again from the config file at the next restart; with a store every apply is written to it, and the draft has
  no `admin`.
- **The config was changed elsewhere**: on `412` the page says to read it again; to keep the changes here, copy
  them out first, then reread.

## In .conf

This is a JSON page, so there is no .conf example here; the .conf form and the three formats of a whole
config are in [the .conf format](../reference/fit.md) and [the formats](../configuration.md#the-formats).
The JSON above is what the page shows.

## See also

- [The config](../configuration.md): [the formats](../configuration.md#the-formats),
  [several files and directories](../configuration.md#several-files-and-directories),
  [changing a running config](../configuration.md#changing-a-running-config).
- [The config reference](../reference/config.md) and [the .conf format](../reference/fit.md).
- [The console and the store](../console.md#the-pages): where this page sits in the menu.
- The [Global settings](07-global-settings.md) chapter's Import / Export page: the whole config as `.conf`,
  `.yaml`, `.json` and `.store`.
- Neighbouring chapters: [the function navigation bar](03-navigation.md), [Versions](09-versions.md),
  [Consistency check](10-consistency-check.md).
