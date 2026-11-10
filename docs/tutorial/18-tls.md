# TLS

[English](18-tls.md) | [简体中文](18-tls.zh-CN.md)

## What it does

The TLS module has three pages: **Certificates** shows the certificates the listeners serve now (file
and ACME alike), read only; **Automatic HTTPS** and **ACME** edit the config's `auto_https` and `acme`
members. A listener's own `tls` (its certificate files, the names in `tls.acme`, on-demand issuance,
client certificates) is set on the [Listeners](05-listeners.md) page; this module only lists what it
serves now.

HTTPS by default is what happens when a config writes no listeners: it is served over HTTPS on 443,
with certificates obtained with ACME for the host names its routes match, and over HTTP on 80, which
only redirects to HTTPS and answers ACME's HTTP-01 challenge. A listener written with an empty `tls`
(`{}`) gets the certificates of the host names of the routes it serves. Names that no public CA
certifies (`localhost`, IP addresses, `*.internal`, wildcards) get none.

ACME says where certificates come from and how they are renewed: the CA's directory (Let's Encrypt's by
default), the account's email, the storage (a directory, or Redis, etcd or Consul as
`shared_storage` for instances on several hosts), the challenges answered (`http-01` and
`tls-alpn-01`), and `on_demand`, which names a URL to ask before a certificate is obtained during a
handshake. `acme` is read when fit starts, so a change to it takes effect when fit restarts; the names
in `tls.acme` follow every reload.

## Pages

### Certificates (`#/gw.tls/certs`)

The read-only table of the certificates, refreshed every 30 seconds. The columns: Listener, Names,
Issuer, Valid from, Expires with the days left (within 14 days it is marked warn, expired is marked
danger), and Serial. The callout at the top says that a listener's `tls` is set on the Listeners page
and that ACME and automatic HTTPS have their own pages. When no listener serves a certificate the page
says so.

This page edits no config: it reads the running state (the Admin API's `GET /status`). The
certificates come from a listener's `tls.certificates`, `tls.acme` and `tls.on_demand`, or from HTTPS
by default. The same table is on the [Overview](04-overview.md) page, whose certificate tile counts
those expiring within 14 days.

What can be done on this page is only looking: no form, no Save and apply. To change what a listener
serves, edit its `tls` on the Listeners page; to have the certificates come from ACME, edit `acme` here,
or write a listener's `tls` empty.

### Automatic HTTPS (`#/gw.tls/auto_https`)

Edits the `auto_https` member: the form plus the `.conf`, `.yaml` and `.json` tabs, and Save and apply
checks and applies it. Its four fields — `disable`, `disable_redirects`, `http_port` and `https_port` —
are in the [key parameters](#key-parameters) below. The page creates no listeners: it changes the rule,
and fit makes the listeners from the rule. The [Engine](06-engine.md) page links each port's TLS
settings: a port whose listener sets `tls` lists "TLS certificates", opening the Certificates page, and,
when its `tls.acme` names hosts, "ACME certificates", opening the ACME page; a port that serves HTTP
with an empty `tls` (as `tls {}`), or the generated port 443, lists "Automatic HTTPS" while HTTPS by
default is on; and the generated port 80 lists
"Redirects to HTTPS"; both open this page.

### ACME (`#/gw.tls/acme`)

Edits the `acme` member: the CA's directory, the email, `ca_file`, `storage` or `shared_storage`
(`redis`, `etcd` or `consul`, one of them), `challenges`, `on_demand`, `max_concurrent` and the two
timeouts. What is changed here takes effect at the next restart; the page edits the config, while the
running issuance and renewal go on by the rules read at start. Until then the
[consistency check](10-consistency-check.md) lists `acme` among the settings whose running value is the
one fit started with.

## Key parameters

`auto_https`:

| field | default | description |
| --- | --- | --- |
| `disable` | off | Turn HTTPS by default off: no listeners are made for a config that writes none, and an empty `tls` gets no certificates. |
| `disable_redirects` | off | Make no listener in the clear to redirect to HTTPS; HTTP-01 is then answered by none, and certificates come through TLS-ALPN-01. |
| `http_port` | `80` | The port of the listener in the clear that redirects to HTTPS. |
| `https_port` | `443` | The port of the HTTPS listener, and the one redirects name when it is not 443. |

`acme` (the key fields):

| field | default | description |
| --- | --- | --- |
| `directory` | Let's Encrypt | The URL of the CA's ACME directory; `acme-staging-v02` is its staging CA, for tests. |
| `email` | none | The address the CA may write to about the account's certificates. |
| `ca_file` | the system's roots | A PEM file of the certificates the directory's is checked against, in place of the system's roots, for a CA of one's own. |
| `storage` | `$XDG_DATA_HOME/fit`, or `~/.local/share/fit` | The directory accounts, keys and certificates are kept in, which instances of fit may share. |
| `shared_storage` | none | Keep them in Redis, etcd or Consul instead (not with `storage`): one instance obtains a certificate, holding a lock there, and the others take it from there; each challenge's answer is kept there too, so that whichever instance the CA reaches answers it. |
| `challenges` | `http-01`, `tls-alpn-01` | The challenges answered, in the order tried: HTTP-01 answered by any listener of fit's in the clear on port 80, TLS-ALPN-01 by the TLS listener that lists the name on port 443. |
| `on_demand` | none | For listeners whose `tls` sets `on_demand`: whom to ask whether a name may have a certificate, the URL `ask`, answered 2xx means yes. |
| `max_concurrent` | `2` | How many certificates are obtained at once; the rest wait their turn. |
| `timeout` | `10m` | How long obtaining one certificate may take, challenges included, before it is given up and tried again later. |
| `request_timeout` | `30s` | How long each request to the CA may take. |

The full field tables are in
[the config reference: HTTPS by default](../reference/config.md#https-by-default) and
[the config reference: ACME](../reference/config.md#acme).

### The challenges and the ports

The challenges are tried in the order written: `http-01` is answered by any listener of fit's in the
clear on port 80 — so with `disable_redirects` on, HTTP-01 is answered by none and certificates can only
come through TLS-ALPN-01 — and `tls-alpn-01` by the TLS listener that lists the name, on port 443. A
failed attempt is tried again with the next. A listener that requires client certificates (`client_auth`
with `mode: require`) cannot answer TLS-ALPN-01, because the CA sends none: with `challenges` set to
`tls-alpn-01` alone, such a listener whose certificates come from ACME (`tls.acme`, `on_demand` or an
empty `tls`) fails validation; set `mode: verify_if_given` or allow `http-01`.

### Renewal

A certificate is renewed once a third of its lifetime is left (a little earlier for some, by its serial
number, so that certificates obtained together are not all renewed together), or earlier when the CA's
renewal information (ARI, RFC 9773) suggests a window. A failed attempt is tried again after a minute,
then twice as long each time, up to an hour. A certificate in the storage is used as it is until it is
due, so a restart obtains nothing again. OCSP responses are fetched and stapled to every certificate ACME
obtained, and to a certificate from files whose chain verifies against the system's roots; this has no
setting.

### Storage and sharing

`storage` is a directory, which instances of fit may share. Across hosts, use
`shared_storage` — one of `redis`, `etcd` or `consul`, and not with `storage`: one instance obtains a
certificate while holding a lock there and the others take it from there; each challenge's answer is kept
there too, so whichever instance the CA reaches can answer it. `prefix` (default `fit/`), `lock_ttl`
(default `30s`, at least `10s`) and `request_timeout` (default `10s`) are the shared fields.

### Issuing on demand

`on_demand` has one required field, `ask`: a URL fit asks with the name in the query as
`domain=<name>`; an answer of 2xx allows the certificate and any other refuses it, and refusals are
remembered for a minute. At most 8 asks are under way at once, 5 a second on average and 20 at once,
however they are answered; a handshake past them fails without asking. `rate` (default 10) and `interval`
(default `1m`) bound how many certificates may be obtained each interval, `max_names` (default 1000) how
many names may have one at all, `timeout` (default `10s`) how long a handshake waits, and `idle_timeout`
(default `168h`) how long a name is kept with no handshake asking for it — after that it no longer counts
against `max_names`, its certificate stays in the storage, and the next ask must allow it again.

## In .conf

One HTTPS listener, the names in `tls.acme`, `auto_https` and the top-level `acme`. This is a
self-contained complete config, and it passes `fit validate`:

```fit
auto_https { http_port 80; https_port 443 }

acme {
	email ops@example.com
	storage /var/lib/fit
	challenges http-01 tls-alpn-01
	on_demand { ask http://127.0.0.1:5555/allowed }
}

listener https :443 {
	tls {
		acme example.com www.example.com
		on_demand true
	}
}

route { handler respond { body hello } }
```

Two things to note: `tls.on_demand` needs the top-level `acme.on_demand` with its `ask`, and without it
validation fails; and the `cert_file` and `key_file` of `tls.certificates` are read during validation, so
a file that does not exist is an error — which is why the example names only ACME names.

For instances on several hosts, use `shared_storage` in place of `storage`: here Redis, its password read
from a file at each connection. This is a complete config too:

```fit
acme {
	email ops@example.com
	shared_storage {
		redis { address 10.0.0.5:6379; password_file /etc/fit/redis-password }
		prefix fit/
		lock_ttl 30s
	}
}

listener https :443 { tls { acme example.com www.example.com } }

route { handler respond { body hello } }
```

Every instance names the same store and `prefix`; `etcd { endpoints http://10.0.0.6:2379 }` or
`consul { endpoint http://127.0.0.1:8500 }` take Redis's place the same way.

## See also

- [Listeners](05-listeners.md): a listener's `tls`, `client_auth` and on-demand certificates.
- [Engine](06-engine.md): where a port lists the certificates, ACME and automatic HTTPS.
- [Overview](04-overview.md): the certificates expiring within 14 days.
- [Consistency check](10-consistency-check.md): a name a listener's `tls.acme` lists that no certificate
  covers is drift, and an `acme` changed since fit started is listed until it restarts.
- [the config reference: HTTPS by default](../reference/config.md#https-by-default): every field of `auto_https`.
- [the config reference: ACME](../reference/config.md#acme): every field of `acme`.
- [the auto_https module doc](../../schema/doc/auto_https.md) and [the acme module doc](../../schema/doc/acme.md).
- [the complete config template](../../templates/conf/conf.d/20-tls.conf): every field, commented.
