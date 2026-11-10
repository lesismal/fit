# Login

[English](02-login.md) | [简体中文](02-login.zh-CN.md)

## What it does

Login is the console's first page: a name and a bcrypt hash, the hash written in the bootstrap file's
`admin.console.users`, passwords never in the clear. A successful login gives a session, and every request
after it is carried by the session, not by the password.

What protects the login is this: failed logins lock an address and slow a user down; the console keeps only
the SHA-256 of a session; every request but a read must carry the session's CSRF token; and the pages allow
their own files only. Checking a password takes bcrypt's tens of milliseconds, so it never runs on a fib
worker but on the console's own bounded pool, and logins cannot hold up logged-in users' requests.

This chapter covers the login page and `POST /api/login`, sessions, the default-password flow, lockouts and
slowdowns, CSRF and the response headers, and the `admin` log. Changing passwords is the
[Account](13-account.md) chapter.

## Pages

### The login page

Opening the console's address while not logged in shows the login page: a **Username**, a **Password**
and **Log in**. It is the only page drawn: every `/api/` request but the login gets `401`
(`log in first`). It sends a JSON body to `POST /api/login` (`{"username", "password"}`, 4 KiB at
most, and it must be `application/json`; anything else gets `415` or `400`), and on success gets
`{"user", "csrf"}` — and, while a temporary password must be changed,
`{"must_change_password": true, "password_expires": ...}` — and the session cookie. The page keeps
the user and the token, and from then every request but a GET or a HEAD carries the token in
`X-Fit-Csrf`. After logging in, `GET /api/workbench` lists the modules this fit serves, and only
then can the menu draw a page.

A failed login shows "Login failed": a `401` says the name or password is wrong, and a `429` says
the address or the user is still waiting, with `{"error", "retry_after"}`, the seconds to wait, and
the same in `Retry-After`. Scripts use the same endpoint: a program keeps the cookie itself and
sends `csrf` in `X-Fit-Csrf` with every request but a read; `GET /api/session` answers
`{"user", "csrf"}` for the same session, or `401` when not logged in; and `POST /api/logout` ends
the session (with `204`, and the cookie cleared).

### Change the default password

A session that logged in with the default password of `fit run -admin` is temporary: it can open only the
**Change the default password** page, and every other endpoint answers `403` with
`"must_change_password": true`, while the Admin API answers every request `403`. That page is the login
page's layout with fields for the current (default) password, **New password** and **Confirm the new
password** — at most 72 bytes, bcrypt's limit — and the time left before the default password stops
working, counted down; **Change password and continue** submits it. The new password must differ from the
current one. Only after it is changed does the workbench become usable. After `-timeout` the password no
longer logs in (`401`, `"password_expired": true`), the page replaces the form with the note that only a
restart with `-admin` helps, and fit must be restarted with `-admin` to get a new one and change it in time.

### Sessions

A login gives a session: 32 random bytes in the cookie `fit_console_<port>` (the console's port, so that
consoles of several fits on one host keep their logins apart), `HttpOnly`, `SameSite=Strict`, `Secure`
over TLS, and cleared at logout. The console keeps only its SHA-256, and ends the session when there is no
request for `session_timeout`, when it has lasted `session_lifetime` in all, at logout, or when
`max_sessions` is passed and it is the oldest. Sessions are in memory: a restart logs everyone out. A
connection is closed 5s after it opens, whatever it sends, until it sends a request with a valid session (a
login that succeeds counts); from then it is kept 2m idle, until a logout or a request finds its session
ended, when it is closed 5s later unless a valid session comes first.

Changing a password ends that user's other sessions, the caller's own kept (see
[Account](13-account.md)).

### Lockouts and slowdowns

- **An address.** A client address that fails 5 times in 15 minutes is refused for 15 minutes (`429` and
  `Retry-After`); before it has been quiet for 15 minutes, each further lockout it earns lasts twice as
  long, up to an hour. An IPv6 address counts by its /64. One check from an address runs at a time, so
  guesses sent at once cannot slip past the count; another sent meanwhile gets `429`. The table of addresses
  holds 4,096 at most, and filling it never ends a lockout early: while it is full of locked-out addresses,
  new ones are refused until the first lockout ends.
- **A user.** A user whose password fails 10 times in a row, from any addresses, has its checks refused for
  2 seconds, then twice as long after each further failure, up to 5 minutes. Once the wait is over the right
  password works and ends the count, and so do 15 minutes without a failure. Names that are no user's share
  one count. Guessing from many addresses is thus held to one guess every 5 minutes, and keeps the user out
  for 5 minutes at most after a guess.
- **The dummy hash.** A name that is not a user is checked against a dummy hash, so the time taken does not
  tell names apart; at most two checks run at once, the rest wait. Logins are checked on a pool of their own
  (1 running, 16 waiting, then `503`). A wrong current password when changing a password counts too, towards
  the lockout and the slowdown.

### CSRF, the Origin and the headers

Every request but a read must carry the session's CSRF token in `X-Fit-Csrf`, or gets `403`; a request whose
`Origin` is not the console's own also gets `403`. Pages are sent with a Content-Security-Policy that allows
their own files only (`script-src`, `style-src` and `connect-src` are `'self'`, `frame-ancestors 'none'`),
with `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff`, and over
TLS `Strict-Transport-Security`; API answers carry `Cache-Control: no-store`. The policy adds the origins
of the listeners that serve the `vod` and `hls` routes to `img-src` and `connect-src`, and adds `media-src`,
so that a media preview can play from them.

### The admin log

The `admin` logger records who logged in and from where, each failed login, each logout, and each change a
user made with the config version it applied. It is written to the output `log` sets, as fit's own log is.

## Key parameters

The fields of `admin.console` (the full tables are in the [Admin API](../reference/config.md#admin-api)
chapter of [the config reference](../reference/config.md)):

| field | default | description |
| --- | --- | --- |
| `address` | `127.0.0.1:2020` | The host and port the console listens on. A host that is not a loopback address needs `cert_file` and `key_file`. |
| `cert_file`, `key_file` | | PEM files with the console's certificate and its private key; with them the console is served over TLS, 1.2 or later. Both are set together or neither. |
| `users` | | Required. Who may log in: at least one user, each with a `username` and a `password_hash`. |
| `session_timeout` | `30m` | How long a login lasts without a request. |
| `session_lifetime` | `12h` | How long a login lasts at most, however busy. |
| `max_sessions` | 64 | The most logins kept at once; one more ends the oldest. |
| `max_conns` | 32 | The most connections open at once, a quarter of them kept for those with a login. When they are taken, a new connection closes the oldest one without a login, of the client holding the most (the host's own clients count as one), rather than being refused; one is refused only when every connection has a login, or the client already holds its share (but for loopback). |
| `access` | | The client addresses the console serves, deciding before the global `access`, and following every config applied, without a restart. |

Beside these, the console has a few fixed limits: request headers 16 KiB, read within 10s; a request body of
`admin.max_config_bytes` plus 64 KiB, read within 30s; writing an answer 30s; a connection closed 5s after it
opens, whatever it sends, until it sends a request with a valid session (a login that succeeds counts), then
kept 2m idle; at most 2 password checks at once; and 4 running and 64 waiting Admin API calls and role
methods, each waiting at most 30s, with `503` past that. See [limits and timeouts](../console.md#limits-and-timeouts).

## In .conf

The login part of the bootstrap file, with a minimal listener and route:

```fit
# The bootstrap file: admin is read when fit starts, and changing it takes a restart.
admin {
	store {
		path /var/lib/fit/fit.store
		password_file /etc/fit/store-password
	}

	console {
		address 127.0.0.1:2020

		users {
			username admin
			password_hash "$2a$10$sKGBAOOCI4p4WWJcVCuAH.ivT8pNy2DFbs6pd8xmtNEZ/5L1SeLvm"
		}
		users {
			username ops
			password_hash "$2a$10$R3sQ0a3nB0eS0dC1kZmV2uLqXlN5pT7yH8gD6fC.bA1iJ2kL3mN4o"
		}

		session_timeout 30m
		session_lifetime 12h
		max_sessions 64
		max_conns 32
	}
}

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

- [Deployment](01-deployment.md): the previous chapter, the bootstrap file and the store.
- [Account](13-account.md): changing passwords after logging in.
- [The function navigation bar](03-navigation.md): the pages after login.
- [The console and the store](../console.md): [Logins and sessions](../console.md#logins-and-sessions),
  [Users and fit hash-password](../console.md#users-and-fit-hash-password),
  [Limits and timeouts](../console.md#limits-and-timeouts).
- [Decision 027](../decisions/027-console-and-store.md): the design of the console and the store.
