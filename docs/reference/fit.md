# .konf reference

[English](fit.md) | [简体中文](fit.zh-CN.md)

A `.konf` file is fit's own config format: blocks of directives, as nginx's and Caddy's configs
are written, saying what fit's native JSON says, field for field. Every field the
[config reference](config.md) lists, and every module's params, can be written in it; a `site`
block writes the routes of a set of host names together. A file ending in `.konf` is read as one
by `fit run`, `fit validate` and `fit adapt`, and a config may be split across several files and
directories ([below](#several-files-and-directories)).

- [A first look](#a-first-look)
- [Syntax](#syntax)
- [Fields](#fields)
- [Modules](#modules)
- [Sites](#sites)
- [Several files and directories](#several-files-and-directories)
- [Import and include](#import-and-include)
- [Errors](#errors)
- [Converting](#converting)

## A first look

```fit
listener web {
	address :8080
	protocols http/1.1 h2c
}

upstream app {
	addresses 127.0.0.1:9000 127.0.0.1:9001
	health_check {
		path /healthz
		interval 5s
	}
}

site example.com www.example.com {
	listeners web
	middleware compress

	route /api/* {
		middleware rate_limit { rate 100; burst 200 }
		handler proxy { upstream app }
	}
	route {
		handler static { root /srv/www }
	}
}
```

The same in JSON: a listener, an upstream, and two routes, each matching the site's hosts, the
first `/api/*` too, each with `compress` first:

```json
{
  "listeners": [{"name": "web", "address": ":8080", "protocols": ["http/1.1", "h2c"]}],
  "upstreams": [{"name": "app", "addresses": ["127.0.0.1:9000", "127.0.0.1:9001"],
                 "health_check": {"path": "/healthz", "interval": "5s"}}],
  "routes": [
    {"match": {"host": ["example.com", "www.example.com"], "path": ["/api/*"]},
     "listeners": ["web"],
     "middleware": [{"module": "compress"}, {"module": "rate_limit", "params": {"rate": 100, "burst": 200}}],
     "handler": {"module": "proxy", "params": {"upstream": "app"}}},
    {"match": {"host": ["example.com", "www.example.com"]},
     "listeners": ["web"],
     "middleware": [{"module": "compress"}],
     "handler": {"module": "static", "params": {"root": "/srv/www"}}}
  ]
}
```

## Syntax

- **A directive** is a name and its arguments, separated by spaces or tabs, on one line; a
  block in braces may follow. A new line ends a directive, and so does a `;`, as in
  `rate 100; burst 200`.
- **Blocks** open with a `{` after the arguments, on the same line, and close with a `}` that
  ends the directive. A block may be written on one line, `{ rate 100 }`, and `{}` is an empty
  one.
- **Comments** run from a `#` that begins a word to the end of the line.
- **Quotes.** `"Double quotes"` hold spaces and `;`, with the escapes `\"`, `\\`, `\n`, `\t`,
  `\r`, and `\{` for a `{` that does not begin `{$`. `` `Backquotes` `` hold anything as it is,
  on several lines too. A quoted argument is always a string.
- **Environment variables.** `{$NAME}` is replaced with the variable's value when the file is
  read, and `{$NAME:default}` with the default when it is unset; not within backquotes.
- **Words** are anything else up to a space or a new line: `{path}`, `/api/*`,
  `https://{host}{uri}` and `text/html;charset=utf-8` are each one word. A word ending in `;`
  ends its directive.

## Fields

Each directive is a field of the block it is in, named as in JSON and YAML; a block is an object.
What follows a field's name depends on its type:

| The field's type | Written as | JSON |
| --- | --- | --- |
| string, duration | one argument | `address :8080` → `"address": ":8080"` |
| integer, number | one argument | `max_conns 256` → `"max_conns": 256` |
| boolean | `true` or `false`, or nothing for true | `secure` → `"secure": true` |
| list of scalars | the arguments, on one line or repeated | `protocols h2 h3` → `"protocols": ["h2", "h3"]` |
| object | a block of its fields | `health_check { path /healthz }` |
| map | a block of keys, each with its value or values | `set { X-Env prod }` → `"set": {"X-Env": "prod"}` |
| list of objects | a block for each, repeated | `certificates { ... }` twice → two certificates |
| a module | its name, then a block of its params ([Modules](#modules)) | `handler proxy { upstream app }` |

- At the top of the config, the lists of objects are written in the singular, each element's
  `name` as the argument: `listener`, `route`, `upstream`, `service` and `provider`. So
  `listener web { address :8080 }` is `{"name": "web", "address": ":8080"}` in `listeners`. The
  name may be written in the block instead, and a route's may be left out. Within a block, a
  list of objects repeats its own name: `certificates { ... }`, `users { ... }`.
- A list given with no arguments is empty: in `match { query { debug } }`, `debug` is a query
  parameter that must be there, with any value.
- A value is read as the field's type has it: `level 5` is a number where the field is one, and
  `body 5` the string `"5"`. A value of the wrong type is reported where it is written, as
  `"100"` quoted for a number.
- An old, renamed field's name still works, with a warning, as in JSON.

## Modules

A route's `handler` and each of its `middleware` name a module, then give its params in a
block, which the module's own fields make up; a module with no params needs no block.
`middleware` is repeated, once for each, in the order they run:

```fit
listener web {
	address :8080
}

upstream app {
	addresses 127.0.0.1:9000
}

route api {
	match {
		path /api/*
	}
	middleware compress
	middleware headers {
		response {
			set {
				Strict-Transport-Security "max-age=31536000"
			}
			delete Server
		}
	}
	handler proxy {
		upstream app
		retry {
			attempts 2
			statuses 502 503 504
		}
	}
}
```

Modules of your own, built in with `fit build`, are written the same way: their params are
read by their own fields.

## Sites

A `site` writes the routes of a set of host names together. It holds what its routes share, and
the routes themselves:

```text
site <hosts...> {
	listeners <names...>    the routes' listeners, where a route names none
	priority <n>            their priority, where a route sets none
	match { ... }           what every route matches too, but host and path;
	                        a route's own match setting the same field wins
	middleware <module> ... middleware every route runs, before its own
	route [<paths...>] { ... }
}
```

Each `route` in a site becomes a route of the config: its `match.host` is the site's hosts, its
`match.path` the route's arguments, when it has any, and the rest is as a top-level route's:
`name`, `match`, `middleware`, `handler`, `priority`, `listeners`. Its routes keep the place the
site is written at among the config's routes. A site's route may not set `host`, nor `path` when
it has paths as arguments.

```fit
listener web {
	address :8080
}

upstream app {
	addresses 127.0.0.1:9000
}

site api.example.com {
	listeners web
	match {
		remote_ip 10.0.0.0/8
	}
	middleware jwt {
		jwks_url https://auth.example.com/.well-known/jwks.json
		audience api
	}

	route /v1/* /v2/* {
		name api
		match {
			method GET POST
		}
		handler proxy { upstream app }
	}
	route /healthz {
		handler respond { body ok }
	}
}
```

is two routes: `api`, matching the host, the two paths, GET and POST and the client range, with
`jwt`; and one for `/healthz`, matching the host, its path and the client range, with `jwt`
too.

## Several files and directories

`-c` may be given more than once, and may name a directory; the config is all the files merged:

```bash
fit run -c /etc/fit/base.konf -c /etc/fit/sites
```

- **A directory** stands for its files ending in `.konf`, `.yaml`, `.yml` or `.json`, in its
  subdirectories too, in lexical order; files and directories whose names begin with a `.` are
  left out. A file named with `-c` is read whatever its name, in the format its extension says (JSON when
  none does), and a file named twice is read once. An nginx config or a Caddyfile is not loaded:
  convert it with `fit adapt -from nginx` or `-from caddyfile` first.
- **Merging.** The lists at the top of the config, `listeners`, `routes`, `upstreams`,
  `services` and `providers`, are joined in the order the files are read. Any other field,
  `admin` or `acme` say, is given in one file only; `schema_version` may be given in several, if
  they agree. Each file may be in any of the formats.
- **Errors** name the file and the place in it, and the path within that file, as
  `sites/api.konf:12:3: routes[0].handler.params.status: ...`.
- **Reloading.** SIGHUP reads every file again. With `-watch`, a file changed, added to a
  directory or taken out of one applies the config again. Every file is read again each time;
  bounds: 10,000 files, and 64 MiB in all.

## Import and include

A `.konf` file may name other files itself, with two directives:

- **`import <paths...>`**, at the top of a config file only, imports whole config files, as more
  `-c` would: each path a file in any of fit's formats, a directory standing for its config
  files, or a glob. They are merged after the file importing them, in the order named, by the
  rules [above](#several-files-and-directories).
- **`include <paths...>`**, anywhere, at the top or in any block, reads the `.konf` directives
  of the files named in its place, as nginx's `include` does: listeners, upstreams or sites at
  the top, what a site's routes share in a site, middleware or a handler in a route. Each path is a file,
  whatever its name, or a glob, whose files are read in lexical order. A file included may
  include others, but not itself, through others or not; it imports nothing.

A path is relative to the directory of the file naming it. A glob that matches nothing names
nothing; a path that is not there is an error.

```text
# fit.konf
import upstreams.yaml sites
include listeners.konf

# sites/api.konf
site api.example.com {
	include ../snippets/site-common.konf
	route /v1/* {
		include ../snippets/secure.inc
		handler proxy { upstream api }
	}
}

# snippets/secure.inc
middleware headers { response { delete Server } }
middleware rate_limit { rate 100 }
```

A file included is read only where it is included, not as a config of its own, also when a
directory given or imported holds it, so snippets may sit beside the sites. An error in a file
included names that file and the place in it. Every file imported or included is watched with
the others: one changed applies the config again, and the bounds on files and bytes count them
too.

## Errors

A `.konf` file is read strictly. A syntax error, a directive in the wrong form (a value for a
block, two values for one), or a field given twice is an error at its line and column; so is
everything the config's own check finds, an unknown field, a value out of range or a missing
one, reported where it was written:

```text
fit.konf:7:10: routes[0].handler.params.status: 99 is less than the minimum 100
```

## Converting

`fit adapt` writes a config in any of fit's formats as a `.konf` file, and back:

```bash
fit adapt -c fit.json -to fit -o fit.konf
```

```bash
fit adapt -c fit.konf -o fit.json
```

Routes are written one by one, not gathered into sites. An nginx config or a Caddyfile converts
to a `.konf` file the same way; see [migrating from nginx](../migrating-from-nginx.md) and
[from Caddy](../migrating-from-caddy.md). [templates/konf/fit.konf](../../templates/konf/fit.konf)
holds every field, with its description and choices in comments.
