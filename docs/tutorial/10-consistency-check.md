# Consistency check

[English](10-consistency-check.md) | [简体中文](10-consistency-check.zh-CN.md)

## What it does

The running fit knows what it actually runs: which listeners are bound, which roles are running, which
certificates are being served, which cache zones exist, the limits the engine actually uses, and what each
provider provided. The config it runs from — the effective config — says what it should run. The two may not
agree, and this page lists every difference so that each one is handled on purpose.

Every difference has a kind: **drift** is an inconsistency a config change can fix, and the server offers one
or more fixes for it; **info** is a known runtime fact, not an inconsistency, and has no fix.

The page reads the Admin API's `GET /config/check`, and the fixes are applied through
`POST /config/reconcile`: the selected fixes go through the same path as saving, so they are checked first,
then applied as one new config version and written to the store.

## Pages

### Consistency check

The tools show the running config's version, when this check ran, and **Check again**. Below them, when there
is drift, two badges: how many drift and how many info; when there is no drift the page says "The running
service and its config agree".

The differences are grouped by category, in this order: Settings, Listeners, Routes, Upstreams, Layer 4
services, TLS certificates, Roles, Cache, Config sources; each group's heading carries its number of items, and
within a category the rows are ordered by name and ID. An element a provider provided is listed under its own
member's category (a provided listener under Listeners), while a provided element that was left out because its
name is already used is info, under Config sources. The two sides compared are the runtime — the bound listeners,
the running roles, the certificates being served, the cache zones, the limits the engine actually uses, and what
the providers provided — and the effective config, which is the base config with the providers merged into it.
Each difference is one row: its kind badge, its name and its detail, then its two sides — the **Config** value
and the **Running** value, each formatted as JSON. Under a drift row there is the **Fix**: a select that picks
one fix, and a checkbox that decides whether to apply it.

A fix's action is one of four:

| action | what the page says | what it does |
| --- | --- | --- |
| `add` | Write it into the config | writes the running value into the config |
| `delete` | Remove it from the config | drops what the config has that does not run |
| `replace` | Change the config to the running value | changes the config to what actually runs |
| `reapply` | Apply the config again | changes nothing and applies it again, to retry or to stop it |

The select shows the action and, when the fix has one, the patch it writes (`Write it into the config · add
/listeners/1`, like that). Where no fix is offered, the page says so: edit the config on its page, or apply the
config again.

The bar at the bottom applies only the rows you ticked: **Apply the selected fixes (N)** opens a confirmation
window listing each difference, its category and the fix picked for it. Nothing is applied until you confirm.
After that the selected fixes go together through the same save path: they are checked first, then applied as
one new config version and written to the store, so a fix that cannot load rejects the whole set; the page says
"Applied as config version V" and the check runs again. If the config was changed elsewhere since this check
(`412`), the page asks you to check again before applying; a `400` error is shown in the toast and the check
runs again.

Each group's heading carries its category and its number of items (1 item / N items); the two summary badges
are the drift count and the info count. The select defaults to its first option, the bar's count follows the
ticks, and with nothing ticked the button is disabled.

After applying, the page checks again: writing the running value into the config makes that difference go away;
but for a setting read only at start, if you change the config's value instead of the running one, the
difference stays until the next restart — the config says the new value and the running service still uses the
one from start.

Beside every difference there is an Ask AI button that asks the skill drift-explain about it (see the
[assistant](14-assistant.md) chapter).

The confirmation window has three columns: **Difference** (the name, with the ID under it when the two differ),
**Category** and **Fix**.

The check runs once when the page is opened and again each time you press **Check again**; it does not refresh
by itself. An unknown category is shown as the server names it.

What a drift row looks like:

```text
drift · Settings · log
  the config sets log differently from the value fit started with, which is in force until fit restarts.
  Config:   {"level": "warn"}
  Running:  {"level": "info"}
  Fix:      [Change the config to the running value · replace /log]   ☐ Apply this fix
```

The endpoints this page uses (the Admin API's endpoints sit under `/api/m/admin/` on the console):

| endpoint | |
| --- | --- |
| `GET /config/check` | the running version, the ETag, when the check ran, the differences and their counts |
| `POST /config/reconcile` | the selected fixes, `{"fixes": [{"id", "action"}]}`, with `If-Match` |

## Key parameters

The fields of a difference (each item of `GET /config/check`):

| field | description |
| --- | --- |
| `id` | A stable name, as `setting:log` or `role:llm.hub`, the same across checks |
| `kind` | `drift` or `info` |
| `category` | settings, listeners, routes, upstreams, services, tls, roles, cache or providers |
| `name` | The difference's subject, as the config or the runtime writes it |
| `detail` | One line saying what it is |
| `config` | The config's value, when it has one |
| `running` | The running value, when it has one |
| `fixes` | The fixes offered: each a `label` (the server's own wording), an `action`, and, for `add`, `delete` and `replace`, the `patch` it writes (`op`, `path`, `value`) |

## Kinds of differences

| Kind | What it can find |
| --- | --- |
| Settings read only at start | `log`, `access_log`, `telemetry`, `engine`, `acme` or `providers` changed after fit started: the config says one thing and the running service still uses the values from start. The fix is to write the running values back into the config (or keep the config and wait for a restart). `limits.max_conns`, `max_pending_bytes` and `max_write_queue_bytes` are the same, taken from the running engine. `admin` is only info: it comes from the bootstrap file |
| Roles | A role the config sets but that is not running (it failed to start): remove it from the config, or apply the config again to retry starting it. A role still running that the config no longer sets: apply the config again to stop it |
| The providers' parts | Elements a provider (`docker`, `consul`, `consul_kv`, `etcd`, `kubernetes`) added to the effective config but that are not in the base config: write them into the config (`listeners`, `routes`, `upstreams`, `services`), or leave them to the provider; they are listed under the member they belong to. Shadowed provider elements are info, under Config sources |
| Listeners | A listener in the config that is not bound, or a bound listener the config no longer has, is drift; a listener whose address is port `0` reports the port the kernel chose, which is info (a dynamic port is deliberate) |
| TLS | A name the base config writes in a listener's `tls.acme` that no certificate served covers is drift, and the fix removes the name from the config; names HTTPS by default added while their certificate is obtained, and certificates served that no name asked for (on-demand TLS, or a certificate from a file), are info |
| Upstreams | An upstream in the config with no pool for it, or a pool whose upstream the config no longer has, is drift; an upstream with all its backends down is info |
| Cache | A cache zone the config uses that does not exist at runtime is drift; a zone kept from an earlier config is info |

The check's answer carries `summary` (the drift and info counts) and `checked_at` (when it ran), which is what
the page's summary and its tools show, and the running version and ETag, the one `If-Match` uses.

## Common cases

- **A setting read only at start was changed**: `log`, `access_log`, `telemetry`, `engine`, `acme` or
  `providers` changed while fit runs. Use `replace` to write the running value back into the config, or keep the
  config and wait for a restart; either works, and the choice is which side you want to be right.
- **An element a provider provided should become part of the config**: use `add` to write it into
  `listeners`, `routes`, `upstreams` or `services`; if it should stay the provider's, tick nothing and leave it.
- **A role failed to start**: use `reapply` to apply the config again and retry it; a role still running that
  the config no longer sets is stopped with `reapply` — the page offers no other fix there.
- **Looking without changing**: tick nothing and this page applies nothing; the confirmation window is the last
  gate.
- **After applying, the check runs again**: the fixes go through the same path as saving, so one that cannot
  load rejects the whole set; the successful one is a new config version, written to the store and recorded in
  the `admin` log.
- **This page's check is not the check when saving**: this page compares the running services with the
  effective config; **Check** on the other pages takes a draft through loading and compiling and tries its new
  listener addresses, but applies nothing. The two complement each other: this one says what is running differs,
  that one says whether a draft can be applied.

- **Picking fixes in the terminal**: `fit check -fix` is the same thing as the page's select plus tick — one fix
  per difference, applied together; both go through `POST /config/reconcile`.

## In the terminal: fit check

`fit check` does the same through the Admin API:

```sh
fit check -c /etc/fit/fit.json          # lists the differences; exit code 1 when there is drift
fit check -c /etc/fit/fit.json -json    # prints the check as JSON
fit check -c /etc/fit/fit.json -fix     # picks a fix per difference, then applies them together
```

`-timeout` (30s by default) bounds how long `fit check` waits for the Admin API. `-json` and `-fix` are given
one at a time.

`admin.address` (and `token_file`) in the bootstrap file says where the Admin API is. With `admin.cert_file`
set, `fit check` uses https and verifies the API's certificate: when it can read `cert_file` (as on the host
fit runs on) it verifies with that certificate and the system CAs, otherwise with the CAs in `-ca`; the name
verified is the host in the address, or localhost when the address names none, and `-server-name` can name it.
With `admin.client_ca_file` set, `-cert` and `-key` must give the client certificate it presents.

Exit codes: `0` when there is no drift; `1` when drift remains (including after `-fix`); `2` for a usage,
config loading, connection or decoding error.

## In .conf

This page edits no member, so there is no .conf example: it reads the running fit and the effective config, and
writes the fixes you picked. The fixes go through the same path as saving, so they are held to the same checks
as `fit validate` — one that cannot load rejects the whole set.

## See also

- [The console and the store](../console.md):
  [checking the running config](../console.md#checking-the-running-config).
- [The config reference](../reference/config.md): [The config](../reference/config.md#the-config) and
  [Admin API](../reference/config.md#admin-api).
- [The config](../configuration.md#changing-a-running-config): the path an apply takes.
- [JSON](08-json.md): the effective config and its sources; [Versions](09-versions.md): the configs applied.
- Neighbouring chapters: [the function navigation bar](03-navigation.md), [Overview](04-overview.md),
  [Logs](11-logs.md).
