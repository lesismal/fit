# Deployment

[English](01-deployment.md) | [简体中文](01-deployment.zh-CN.md)

## What it does

fit is a static binary plus a config file: install it, write a config, and `fit run` starts it. With a
console the config is split into two files: a plaintext **bootstrap file**, which holds only `admin`,
and a **store** sealed with a password, which holds the rest of the config. The console is the web page
you see after logging in (the [function navigation bar](03-navigation.md)), and every change made from
it is written to the store, so a restart runs from the store again.

This chapter walks through, in order: installing; what each of the two files holds; the first start with
`fit run -admin`; creating users; where the console can be reached; and the Docker, Helm and systemd
packaging notes. Full field tables are not repeated here; they are linked to
[the config reference](../reference/config.md).

## Pages

### Installing

With Go 1.27.3 or later, `go install` builds fit into `$(go env GOPATH)/bin`:

```bash
go install github.com/lesismal/fit/cmd/fit@latest
```

In the source directory `make build` puts the binary at `bin/fit`; `fit version` prints the versions of fit,
fib and Go.

Without Go, download a prebuilt binary from [GitHub Releases](https://github.com/lesismal/fit/releases):
Linux (amd64, arm64, armv7, 386), macOS (amd64, arm64), Windows (amd64, arm64) and FreeBSD
(amd64, arm64), each a `.tar.gz` (a `.zip` on Windows), with the SHA-256 checksums in `checksums.txt`.
The container image `ghcr.io/lesismal/fit` is published too (on a `v<semver>` tag), along with the Helm
chart and the systemd unit. The details of the three packagings (ports, volumes, sysctls, values) are all
in [deployment](../../deploy/README.md); this chapter keeps only the parts that matter to the console.

### The two files

A file cannot hold the password that opens it, so a config with a store is in two files:

- the **bootstrap file**, the one `fit run -c` names, in clear text and in any of fit's formats. It
  holds `admin`: the Admin API, `admin.store` (where the store is and its password) and
  `admin.console` (where the console listens and who may log in);
- the **store**, sealed with the password. It holds everything but `admin`: the **base config** fit runs
  from, before providers are merged into it.

The first time fit starts with this file there is no store yet: the bootstrap file's `listeners` and
`routes` are its first content, and the store is written at once. From then on the store is merged onto
the bootstrap file at each start, the store's values winning: objects are merged member by member, and a
list or a value the store holds replaces the file's whole. A member only the file sets is kept, and the
merged config is written back to the store. To start again from the file, delete the store. A write goes to
a temporary file beside the store, 0600, is synced and then renamed over it, so a crash leaves either the
old or the new store, never half of one.

`admin` never enters the store and is never shown: it is put in at each config load, but not into the
base config, so the Admin API's `GET /config` and the JSON page hold no passwords, hashes or key paths.
With a store, a config that carries `admin` is rejected: `admin` comes only from the bootstrap file.
`admin`'s settings, the store's and the console's included, are read when fit starts; changing them takes
effect at a restart.

A store that does not open, for a wrong password or a changed byte, stops fit from starting and is never
overwritten. So keep a copy: `fit store export -c <bootstrap file>` prints the config it holds as JSON, and
`fit store import -c <bootstrap file> <file>` checks a config and seals it again, replacing what was there
with a new salt — changing the store's password is done this way too; a running fit sees the import only
after a SIGHUP.

### First start: fit run -admin

`fit run -admin` turns the Admin API and the console on with no config for them, for a first start or a
machine set up from the console:

```bash
fit run -admin                       # fit.json when there is one, or no config at all
fit run -admin -c site.yaml -timeout 30m
```

- `admin` and `admin.console` get their defaults where the config leaves them out: the Admin API on
  `127.0.0.1:2019`, the console on `127.0.0.1:2020`. With no `admin.console.users`, the user `admin` logs
  in with the password `admin`.
- That password is **temporary**: it must be changed within `-timeout` (`10m` when left out), counted from
  the start. Until it is changed, a login with it opens only the "change the default password" page (see
  [Login](02-login.md)). After `-timeout` it no longer logs in, and fit must be restarted with `-admin` to
  change it in time. The new password cannot be `admin`, and is kept in the store.
- Without `admin.store`, the store is the file beside the config with `.store` for its extension
  (`site.yaml` gives `site.store`, `fit.json` or no config gives `fit.store`), and its password is
  `$FIT_STORE_PASSWORD` or, without it, the **key file** beside the store, the store's path with `.key`
  added (`site.store.key`), made 0600 with a random key when neither exists yet. Keep the key file with the
  store: a store copied elsewhere opens with it.
- A config with only `admin.console` and no listener or route is valid, so that fit can start with nothing
  to serve yet.
- `-c` takes a `.store` file too, at most one, alone or beside config files: `fit run -c site.store` runs
  what the store holds. A `.store` beside the config files is used once it exists, whether or not `-admin`
  is given.

### Creating users

A user is a name and a bcrypt hash; passwords are never written in the clear. `fit hash-password` prints
the hash of a password: in a terminal it asks for the password twice (each keystroke shows a `*`), and
otherwise reads the first line of its standard input; it never reads a password from an argument, because
the machine's other users can see arguments. `-cost` sets bcrypt's cost (10 by default; each step doubles
the time a check takes). A password is at most 72 bytes and cannot be empty. The same hashes are used for
`basic_auth` users.

```bash
echo 'correct horse battery staple' | fit hash-password
```

Write the hash it prints into the bootstrap file's `admin.console.users`. `fit validate` rejects a
`password_hash` that is not a bcrypt hash, an empty user name, and a name listed twice. After logging in a
password can also be changed on the Account page (see [Account](13-account.md)), but changed passwords are
kept in the store while `admin` comes only from the bootstrap file: the user itself and its first hash stay
in the file.

### Reaching the console

The console is on a loopback address by default, so the usual way in is from the machine itself, or by
tunnelling the port through SSH:

```bash
ssh -L 2020:127.0.0.1:2020 host
```

TLS is optional on a loopback address. **A host that is not a loopback address needs `cert_file` and
`key_file`**, so that passwords never cross a network in the clear; `fit validate` rejects a config without
them. `access` says which client addresses the console serves, and it follows every config applied, without
a restart; writing `allow 127.0.0.1` (and `::1`) in it keeps local access whatever the global `access` says.

### Docker, Helm and systemd

- **Docker**: the image is distroless's static image, run as `nonroot` (65532), holding only
  `/usr/local/bin/fit` and a placeholder config at `/etc/fit/fit.json`. The store's directory must be in a
  volume (a write is a new file renamed over the store), and the password goes in an environment variable
  (`password_env`) or a mounted file (`password_file`), not in the config. To reach the console from outside
  the container, give it a non-loopback address with `cert_file` and `key_file`. Ports below 1024 are let
  through by the sysctl `net.ipv4.ip_unprivileged_port_start=0`, which is what the Helm chart does.
- **Helm**: the chart runs fit as the Gateway API and Ingress controller, with the config in a ConfigMap;
  the console and store settings go in the `config` value, the store must be on a persistent volume, and the
  password comes from a Secret, as a mounted file or an environment variable.
- **systemd**: `fit.service` is for a fit whose config is at `/etc/fit/fit.json`, and `fit@<name>` for
  `/etc/fit/<name>.json`. fit runs as the system user `fit`, and only `/var/lib/fit` (the `StateDirectory`,
  where the store's `path` goes) and `/run/fit` are writable. The config is checked with `fit validate`
  before the start; `systemctl reload fit` checks the new config and sends SIGHUP, and a SIGHUP re-reads the
  store, for example after a `fit store import`. `systemctl stop fit` sends SIGTERM, and fit waits at most
  30s (`-shutdown-timeout`) for requests in flight.

## Key parameters

The fields of `admin.store` (the full tables are in the [Admin API](../reference/config.md#admin-api)
chapter of [the config reference](../reference/config.md)):

| field | default | description |
| --- | --- | --- |
| `path` | | Required. The store's file, made 0600 when it is written. fit does not start when it exists and does not open. |
| `password` | | The password, written in the bootstrap file. It protects a store copied away from this file, not one read beside it. |
| `password_file` | | A file holding the password, its surrounding spaces trimmed. |
| `password_env` | | An environment variable holding the password. |

Exactly one of the three is set; fit does not start when the file is empty or the variable is unset or
empty. The store's format is fit's own `fitstore`: a 59-byte header plus a JSON payload sealed with
XChaCha20-Poly1305, the key derived from the password by Argon2id, once, only at start. A store that does
not open is never overwritten; losing the password loses the store, so keep a copy with
`fit store export`.

The fields of `admin.console` are listed in [Login](02-login.md).

## In .conf

A complete bootstrap file: `admin` (the store and the console) plus a minimal listener and route, which are
the store's first content.

```fit
# The bootstrap file: only admin is written in the clear, the rest of the config is in the store.
admin {
	# The Admin API: the console calls it in the process, and both apply configs the same way.
	address 127.0.0.1:2019

	store {
		path /var/lib/fit/fit.store
		# Set exactly one of password, password_file and password_env.
		password_env FIT_STORE_PASSWORD
	}

	console {
		address 127.0.0.1:2020
		users {
			username admin
			password_hash "$2a$10$sKGBAOOCI4p4WWJcVCuAH.ivT8pNy2DFbs6pd8xmtNEZ/5L1SeLvm"
		}
	}
}

# The store's first content: what the file sets, until the store has its own.
listener web {
	address :8080
	protocols http/1.1 h2c
}

route hello {
	match {
		path /hello
	}
	handler respond {
		body "hello from fit\n"
	}
}
```

## See also

- [Deployment](../../deploy/README.md): the container image, the Helm chart and the systemd unit in detail.
- [Getting started](../getting-started.md): a site built from nothing.
- [The console and the store](../console.md): [Two files](../console.md#two-files),
  [The store](../console.md#the-store), [fit run -admin](../console.md#fit-run--admin),
  [Users and fit hash-password](../console.md#users-and-fit-hash-password).
- [The config](../configuration.md) and the [.conf format](../reference/fit.md).
- [Login](02-login.md): the next chapter; [Account](13-account.md): changing passwords;
  [the function navigation bar](03-navigation.md): the pages after logging in.
- [Decision 027](../decisions/027-console-and-store.md): the design of the console and the store.
