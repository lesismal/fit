package caddyfile

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// placeholder finds Caddy's placeholders, which fit expands only in a few
// places.
var placeholder = regexp.MustCompile(`\{[A-Za-z_][A-Za-z0-9_.\-]*(\[[^\]]*\])?\}`)

// redirectVars are the placeholders fit's redirect has, by Caddy's names.
var redirectVars = strings.NewReplacer(
	"{http.request.uri}", "{uri}", "{http.request.uri.path}", "{path}", "{http.request.uri.query}", "{query}",
	"{http.request.host}", "{host}", "{http.request.hostport}", "{host}", "{hostport}", "{host}",
	"{http.request.scheme}", "{scheme}",
)

// redirectTarget converts a URL of Caddy's to one of fit's redirect, or
// reports false when it has placeholders fit has not.
func redirectTarget(to string) (string, bool) {
	to = redirectVars.Replace(to)
	for _, p := range placeholder.FindAllString(to, -1) {
		switch p {
		case "{uri}", "{path}", "{query}", "{host}", "{scheme}":
		default:
			return "", false
		}
	}
	return to, true
}

// modifier converts a directive that changes the routes it covers.
func (c *converter) modifier(s *site, d *directive) *modifier {
	single := d.name == "root" || d.name == "redir" || d.name == "rewrite"
	match, args, spec, ok := c.matcher(s, d, single)
	if !ok {
		return nil
	}
	m := &modifier{d: d, match: match, spec: spec, order: order[d.name]}
	add := func(module string, params map[string]any) *modifier {
		m.add = &mw{outRef{Module: module, Params: params}, d.line}
		return m
	}
	switch d.name {
	case "root":
		if len(args) != 1 {
			c.skip(d, "root takes a path")
			return nil
		}
		if placeholder.MatchString(args[0]) {
			c.skip(d, "a root with placeholders is not converted")
			return nil
		}
		root := args[0]
		m.set = func(h *handler) bool {
			if h.rootSet {
				return false
			}
			h.root, h.rootSet = root, true
			return true
		}
		return m
	case "try_files":
		files, ok := c.tryFiles(d, args)
		if !ok {
			return nil
		}
		m.set = func(h *handler) bool {
			if h.trySet {
				return false
			}
			h.tryFiles, h.trySet, h.tryLine = files, true, d.line
			return true
		}
		return m
	case "redir":
		if len(args) < 1 || len(args) > 2 {
			c.skip(d, "redir takes a URL and a status")
			return nil
		}
		to, ok := redirectTarget(args[0])
		if !ok {
			c.skip(d, "its URL has placeholders fit has no counterpart for")
			return nil
		}
		status := 302
		if len(args) == 2 {
			switch args[1] {
			case "permanent":
				status = 301
			case "temporary":
			case "html":
				c.warn(d.line, "redir html: written as a 302 redirect")
			default:
				n, err := strconv.Atoi(args[1])
				if err != nil || n < 300 || n > 399 {
					c.skip(d, "a status fit's redirect does not take")
					return nil
				}
				status = n
			}
		}
		return add("redirect", map[string]any{"to": to, "status": status})
	case "rewrite":
		if len(args) != 1 {
			c.skip(d, "rewrite takes a URI")
			return nil
		}
		p, ok := c.rewrite(d, args[0])
		if !ok {
			return nil
		}
		c.warn(d.line, "rewrite: fit picks a request's route before rewriting it: the rewritten path does not choose another route")
		return add("rewrite", p)
	case "uri":
		p, ok := c.uri(d, args)
		if !ok {
			return nil
		}
		return add("rewrite", p)
	case "header", "request_header":
		ops := c.headerOps(d, args, d.block)
		if ops == nil {
			return nil
		}
		side := "response"
		if d.name == "request_header" {
			side = "request"
		}
		return add("headers", map[string]any{side: ops})
	case "encode":
		p, ok := c.encode(d, args)
		if !ok {
			return nil
		}
		return add("compress", p)
	case "basicauth", "basic_auth":
		p, ok := c.basicAuth(d, args)
		if !ok {
			return nil
		}
		return add("basic_auth", p)
	case "forward_auth":
		p, ok := c.forwardAuth(d, args)
		if !ok {
			return nil
		}
		return add("forward_auth", p)
	case "request_body":
		for _, e := range d.block {
			if e.name == "max_size" && len(e.args) == 1 {
				if n, ok := size(e.args[0]); ok {
					return add("body_limit", map[string]any{"max_bytes": n})
				}
			}
		}
		c.skip(d, "fit limits a request body's size, with max_size")
		return nil
	}
	return nil
}

// tryFiles converts try_files's files to fit's, {path} kept, and the last
// =404 dropped, which is what fit answers anyway.
func (c *converter) tryFiles(d *directive, args []string) ([]string, bool) {
	var out []string
	for i, a := range args {
		if strings.HasPrefix(a, "=") {
			if i != len(args)-1 {
				c.skip(d, "=status is not converted but last")
				return nil, false
			}
			if a != "=404" {
				c.warn(d.line, "try_files: the last %s answers 404 in fit", a)
			}
			continue
		}
		a = strings.ReplaceAll(a, "{http.request.uri.path}", "{path}")
		if strings.ContainsAny(a, "?") || strings.Contains(strings.ReplaceAll(a, "{path}", ""), "{") {
			c.skip(d, "a file with a query or with placeholders other than {path} is not converted")
			return nil, false
		}
		if !strings.HasPrefix(a, "/") && !strings.HasPrefix(a, "{path}") {
			a = "/" + a
		}
		out = append(out, a)
	}
	for _, e := range d.block {
		c.skip(e, "fit tries files in order, the first that exists served")
	}
	if len(out) == 0 {
		c.skip(d, "nothing to try")
		return nil, false
	}
	return out, true
}

// rewrite converts rewrite's URI to the rewrite middleware's params.
func (c *converter) rewrite(d *directive, to string) (map[string]any, bool) {
	to = strings.ReplaceAll(strings.ReplaceAll(to, "{http.request.uri.path}", "{path}"), "{http.request.uri}", "{uri}")
	if strings.Contains(to, "?") {
		c.skip(d, "a rewrite of the query is not converted")
		return nil, false
	}
	if !strings.HasPrefix(to, "/") {
		c.skip(d, "fit rewrites to a path starting with /")
		return nil, false
	}
	// The query is kept, so {uri} is the path's.
	to = strings.ReplaceAll(to, "{uri}", "{path}")
	if prefix, ok := strings.CutSuffix(to, "{path}"); ok && !strings.Contains(prefix, "{") {
		if prefix == "" {
			return nil, false
		}
		return map[string]any{"add_prefix": prefix}, true
	}
	if strings.Contains(to, "{") {
		c.skip(d, "a URI with placeholders other than {path} is not converted")
		return nil, false
	}
	return map[string]any{"regexp": "^.*$", "replacement": strings.ReplaceAll(to, "$", "$$")}, true
}

// uri converts uri's operations to the rewrite middleware's params.
func (c *converter) uri(d *directive, args []string) (map[string]any, bool) {
	if len(args) < 2 {
		c.skip(d, "uri takes an operation and its arguments")
		return nil, false
	}
	switch op := args[0]; {
	case op == "strip_prefix" && len(args) == 2:
		p := args[1]
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		return map[string]any{"strip_prefix": []string{strings.TrimSuffix(p, "/")}}, true
	case op == "strip_suffix" && len(args) == 2:
		return map[string]any{"regexp": regexp.QuoteMeta(args[1]) + "$", "replacement": ""}, true
	case op == "replace" && (len(args) == 3 || len(args) == 4):
		if len(args) == 4 {
			c.warn(d.line, "uri replace: its limit is not converted: fit replaces every match")
		}
		return map[string]any{"regexp": regexp.QuoteMeta(args[1]), "replacement": strings.ReplaceAll(args[2], "$", "$$")}, true
	case op == "path_regexp" && len(args) == 3:
		if _, err := regexp.Compile(args[1]); err != nil {
			c.skip(d, "Go's regular expressions do not take "+strconv.Quote(args[1]))
			return nil, false
		}
		return map[string]any{"regexp": args[1], "replacement": args[2]}, true
	}
	c.skip(d, "uri "+args[0]+" is not converted")
	return nil, false
}

// headerOps converts the field operations of header or request_header, on
// their line or in their block: set, +add, -delete.
func (c *converter) headerOps(d *directive, args []string, block []*directive) map[string]any {
	set := map[string]string{}
	add := map[string][]string{}
	var del []string
	op := func(line int, a []string) {
		if len(a) == 0 {
			return
		}
		f := a[0]
		switch {
		case strings.HasPrefix(f, "-"):
			if len(a) != 1 {
				c.warn(line, "%s %s: skipped: a deletion takes no value", d.name, f)
				return
			}
			del = append(del, f[1:])
			return
		case len(a) == 1:
			c.warn(line, "%s %s: skipped: a field with no value", d.name, f)
			return
		case len(a) > 2:
			c.warn(line, "%s %s: skipped: replacing within a value is not converted", d.name, f)
			return
		case placeholder.MatchString(a[1]):
			c.warn(line, "%s %s: skipped: its value has placeholders, which fit does not expand", d.name, f)
			return
		}
		switch f[0] {
		case '+':
			add[f[1:]] = append(add[f[1:]], a[1])
		case '?':
			c.warn(line, "%s %s: written as set: fit sets the field whether or not it is there", d.name, f)
			set[f[1:]] = a[1]
		case '>':
			set[f[1:]] = a[1]
		default:
			set[f] = a[1]
		}
	}
	op(d.line, args)
	for _, e := range block {
		if e.name == "defer" {
			continue
		}
		op(e.line, append([]string{e.name}, e.args...))
	}
	ops := map[string]any{}
	if len(set) > 0 {
		ops["set"] = set
	}
	if len(add) > 0 {
		ops["add"] = add
	}
	if len(del) > 0 {
		ops["delete"] = del
	}
	if len(ops) == 0 {
		c.skip(d, "no field it changes converted")
		return nil
	}
	return ops
}

func (c *converter) encode(d *directive, args []string) (map[string]any, bool) {
	var encs []string
	params := map[string]any{}
	addEnc := func(line int, e string) {
		switch e {
		case "zstd", "gzip", "br":
			if !slices.Contains(encs, e) {
				encs = append(encs, e)
			}
		default:
			c.warn(line, "encode %s: skipped: fit compresses with zstd, br and gzip", e)
		}
	}
	for _, a := range args {
		addEnc(d.line, a)
	}
	for _, e := range d.block {
		switch e.name {
		case "zstd", "gzip", "br":
			addEnc(e.line, e.name)
			if len(e.args) > 0 {
				c.warn(e.line, "encode %s %s: the level is not converted: fit's compress has levels fastest, default and better", e.name, e.args[0])
			}
		case "minimum_length":
			if len(e.args) == 1 {
				if n, err := strconv.Atoi(e.args[0]); err == nil && n >= 0 {
					params["min_bytes"] = n
					continue
				}
			}
			c.skip(e, "minimum_length takes a number of bytes")
		case "match":
			c.skip(e, "fit compresses what compresses well, by its content type")
		default:
			c.skip(e, "not converted")
		}
	}
	if len(args) > 0 || len(d.block) > 0 {
		if len(encs) == 0 {
			c.skip(d, "none of its encodings converted")
			return nil, false
		}
		params["encodings"] = encs
	} else {
		// Caddy's encode with nothing named offers zstd and gzip.
		params["encodings"] = []string{"zstd", "gzip"}
	}
	return params, true
}

func (c *converter) basicAuth(d *directive, args []string) (map[string]any, bool) {
	params := map[string]any{}
	if len(args) > 0 && args[0] != "bcrypt" {
		c.skip(d, "fit checks bcrypt hashes")
		return nil, false
	}
	if len(args) == 2 {
		params["realm"] = args[1]
	}
	var users []map[string]any
	for _, e := range d.block {
		if len(e.args) != 1 {
			c.warn(e.line, "%s: skipped: a user takes a name and a password hash", e.name)
			continue
		}
		hash := e.args[0]
		if !strings.HasPrefix(hash, "$2") {
			// Caddy before 2.8 wrote hashes in base64.
			if b, err := base64.StdEncoding.DecodeString(hash); err == nil && strings.HasPrefix(string(b), "$2") {
				hash = string(b)
			} else {
				c.warn(e.line, "%s: skipped: not a bcrypt hash", e.name)
				continue
			}
		}
		users = append(users, map[string]any{"username": e.name, "password_hash": hash})
	}
	if len(users) == 0 {
		c.skip(d, "no user converted")
		return nil, false
	}
	params["users"] = users
	return params, true
}

func (c *converter) forwardAuth(d *directive, args []string) (map[string]any, bool) {
	addrs := slices.Clone(args)
	var rest []*directive
	for _, e := range d.block {
		switch e.name {
		case "to":
			addrs = append(addrs, e.args...)
		case "uri", "copy_headers", "header_up", "trusted_proxies":
			rest = append(rest, e)
		}
	}
	u, ok := c.upstream(d, addrs, func(_ *outUpstream, e *directive) bool { return slices.Contains(rest, e) })
	if !ok {
		return nil, false
	}
	params := map[string]any{"upstream": u.Name}
	var copied []string
	for _, e := range rest {
		switch e.name {
		case "uri":
			if len(e.args) == 1 && !placeholder.MatchString(e.args[0]) {
				params["uri"] = e.args[0]
				continue
			}
			c.skip(e, "a URI with placeholders is not converted")
		case "copy_headers":
			for _, h := range append(slices.Clone(e.args), names(e.block)...) {
				if strings.Contains(h, ">") {
					c.warn(e.line, "copy_headers %s: skipped: renaming is not converted", h)
					continue
				}
				copied = append(copied, h)
			}
		case "header_up":
			c.skip(e, "fit sends the request's fields to the auth service")
		case "trusted_proxies":
			c.skip(e, "not converted")
		default:
			c.skip(e, "not converted")
		}
	}
	if len(copied) > 0 {
		params["copy_headers"] = copied
	}
	return params, true
}

func names(ds []*directive) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.name)
	}
	return out
}

// handler converts a directive that answers: its handler, and middleware
// that goes with it, as a proxy's header_up.
func (c *converter) handler(s *site, d *directive, args []string) (handler, []mw, bool) {
	switch d.name {
	case "respond":
		return c.respond(d, args)
	case "error":
		status := 500
		for _, a := range args {
			if n, err := strconv.Atoi(a); err == nil && n >= 400 && n < 600 {
				status = n
			}
		}
		c.warn(d.line, "error: answered %d with no body: Caddy's error routes are not converted", status)
		return handler{ref: outRef{Module: "respond", Params: map[string]any{"status": status}}}, nil, true
	case "abort":
		c.skip(d, "fit answers every request it takes")
		return handler{}, nil, false
	case "file_server":
		return c.fileServer(d, args)
	case "php_fastcgi":
		return c.phpFastCGI(d, args)
	case "reverse_proxy":
		return c.reverseProxy(d, args)
	}
	return handler{}, nil, false
}

func (c *converter) respond(d *directive, args []string) (handler, []mw, bool) {
	params := map[string]any{}
	body := ""
	switch len(args) {
	case 0:
	case 1:
		if n, err := strconv.Atoi(args[0]); err == nil && len(args[0]) == 3 {
			params["status"] = n
		} else {
			body = args[0]
		}
	case 2:
		body = args[0]
		n, err := strconv.Atoi(args[1])
		if err != nil {
			c.skip(d, "respond takes a body and a status")
			return handler{}, nil, false
		}
		params["status"] = n
	default:
		c.skip(d, "respond takes a body and a status")
		return handler{}, nil, false
	}
	for _, e := range d.block {
		switch {
		case e.name == "body" && len(e.args) == 1:
			body = e.args[0]
		case e.name == "close":
			c.skip(e, "fit keeps the connection")
		default:
			c.skip(e, "not converted")
		}
	}
	if n, ok := params["status"].(int); ok && (n < 100 || n > 599) {
		c.skip(d, "a status out of range")
		return handler{}, nil, false
	}
	if body != "" {
		if placeholder.MatchString(body) {
			c.warn(d.line, "respond: its body's placeholders are sent as they are written: fit does not expand them")
		}
		params["body"] = body
	}
	if len(params) == 0 {
		params = nil
	}
	return handler{ref: outRef{Module: "respond", Params: params}}, nil, true
}

func (c *converter) fileServer(d *directive, args []string) (handler, []mw, bool) {
	params := map[string]any{}
	h := handler{}
	for _, a := range args {
		if a == "browse" {
			params["browse"] = true
		} else {
			c.warn(d.line, "file_server %s: skipped: not converted", a)
		}
	}
	for _, e := range d.block {
		switch e.name {
		case "root":
			if len(e.args) == 1 {
				h.root, h.rootSet = e.args[0], true
				continue
			}
			c.skip(e, "root takes a path")
		case "browse":
			params["browse"] = true
			if len(e.args) > 0 || len(e.block) > 0 {
				c.warn(e.line, "browse: its template and options are not converted")
			}
		case "index":
			params["index"] = append([]string{}, e.args...)
		case "precompressed":
			var encs []string
			for _, a := range e.args {
				switch a {
				case "br", "zstd", "gzip":
					encs = append(encs, a)
				default:
					c.warn(e.line, "precompressed %s: skipped: fit serves br, zstd and gzip twins", a)
				}
			}
			if len(e.args) == 0 {
				encs = []string{"br", "zstd", "gzip"}
			}
			if len(encs) > 0 {
				params["precompressed"] = encs
			}
		case "hide":
			c.skip(e, "fit hides no files but those starting with a dot in listings")
		default:
			c.skip(e, "not converted")
		}
	}
	h.ref = outRef{Module: "static", Params: params}
	return h, nil, true
}

func (c *converter) phpFastCGI(d *directive, args []string) (handler, []mw, bool) {
	if len(args) == 0 {
		c.skip(d, "php_fastcgi needs the application's address")
		return handler{}, nil, false
	}
	if len(args) > 1 {
		c.warn(d.line, "php_fastcgi: fit sends requests to the first address, %s", args[0])
	}
	addr := args[0]
	switch {
	case strings.HasPrefix(addr, "unix/"):
	case strings.HasPrefix(addr, ":"):
		addr = "localhost" + addr
	default:
		if _, _, err := net.SplitHostPort(addr); err != nil {
			addr = net.JoinHostPort(addr, "9000")
		}
	}
	params := map[string]any{"address": addr, "try_files": []string{"{path}", "{path}/index.php", "/index.php"}}
	h := handler{}
	for _, e := range d.block {
		switch {
		case e.name == "root" && len(e.args) == 1:
			params["root"] = e.args[0]
		case e.name == "split" && len(e.args) > 0:
			params["split_path"] = e.args
		case e.name == "index" && len(e.args) == 1:
			if e.args[0] == "off" {
				delete(params, "try_files")
				continue
			}
			params["index"] = e.args[0]
			params["try_files"] = []string{"{path}", "{path}/" + e.args[0], "/" + e.args[0]}
		case e.name == "try_files" && len(e.args) > 0:
			if files, ok := c.tryFiles(e, e.args); ok {
				params["try_files"] = files
			}
		case e.name == "env" && len(e.args) == 2:
			env, _ := params["env"].(map[string]string)
			if env == nil {
				env = map[string]string{}
			}
			env[e.args[0]] = e.args[1]
			params["env"] = env
		case e.name == "dial_timeout" && len(e.args) == 1:
			if t, ok := duration(e.args[0]); ok {
				params["connect_timeout"] = t
			}
		case e.name == "read_timeout" && len(e.args) == 1:
			if t, ok := duration(e.args[0]); ok {
				params["response_idle_timeout"] = t
			}
		case e.name == "resolve_root_symlink" || e.name == "capture_stderr":
			c.skip(e, "not converted")
		default:
			c.skip(e, "not converted")
		}
	}
	h.ref = outRef{Module: "fastcgi", Params: params}
	return h, nil, true
}

func (c *converter) reverseProxy(d *directive, args []string) (handler, []mw, bool) {
	addrs := slices.Clone(args)
	for _, e := range d.block {
		if e.name == "to" {
			addrs = append(addrs, e.args...)
		}
	}
	params := map[string]any{}
	var reqOps, respOps [][]string
	var reqLines, respLines []int
	u, ok := c.upstream(d, addrs, func(u *outUpstream, e *directive) bool {
		switch e.name {
		case "header_up":
			if len(e.args) >= 1 {
				f := strings.ToLower(strings.TrimLeft(e.args[0], "+"))
				switch {
				case f == "host" && len(e.args) == 2:
					v := e.args[1]
					switch v {
					case "{host}", "{http.request.host}", "{hostport}", "{http.request.hostport}":
						// fit sends the client's Host.
					case "{upstream_hostport}", "{http.reverse_proxy.upstream.hostport}":
						if len(u.Addresses) == 1 {
							params["host_header"] = u.Addresses[0]
						} else {
							c.warn(e.line, "header_up Host %s: skipped: fit sends one Host to every address", v)
						}
					default:
						if placeholder.MatchString(v) {
							c.warn(e.line, "header_up Host %s: skipped: not converted", v)
						} else {
							params["host_header"] = v
						}
					}
				case strings.HasPrefix(f, "x-forwarded-") || f == "x-real-ip" && len(e.args) == 2 && e.args[1] == "{remote_host}":
					c.warn(e.line, "header_up %s: skipped: fit's proxy sets X-Forwarded-For, -Proto and -Host itself", e.args[0])
				default:
					reqOps = append(reqOps, e.args)
					reqLines = append(reqLines, e.line)
				}
			}
			return true
		case "header_down":
			respOps = append(respOps, e.args)
			respLines = append(respLines, e.line)
			return true
		case "trusted_proxies":
			params["trusted_proxies"] = ranges(e.args)
			return true
		case "lb_retries":
			if len(e.args) == 1 {
				if n, err := strconv.Atoi(e.args[0]); err == nil && n > 0 {
					params["retry"] = map[string]any{"attempts": min(n, 10)}
					return true
				}
			}
		case "lb_try_duration":
			if _, ok := params["retry"]; !ok {
				params["retry"] = map[string]any{}
			}
			c.warn(e.line, "lb_try_duration: written as fit's retry, which tries again at once, twice by default, within a budget")
			return true
		case "flush_interval", "buffer_requests", "buffer_responses", "request_buffers", "response_buffers", "max_buffer_size":
			c.skip(e, "fit streams bodies both ways, as they arrive")
			return true
		case "stream_timeout":
			if len(e.args) == 1 {
				if t, ok := duration(e.args[0]); ok {
					params["tunnel_idle_timeout"] = t
					return true
				}
			}
		}
		return false
	})
	if !ok {
		return handler{}, nil, false
	}
	params["upstream"] = u.Name
	var extra []mw
	reqSet := map[string]any{}
	if len(reqOps) > 0 {
		if ops := c.proxyHeaders("header_up", reqOps, reqLines); ops != nil {
			reqSet["request"] = ops
		}
	}
	if len(respOps) > 0 {
		if ops := c.proxyHeaders("header_down", respOps, respLines); ops != nil {
			reqSet["response"] = ops
		}
	}
	if len(reqSet) > 0 {
		extra = append(extra, mw{outRef{Module: "headers", Params: reqSet}, d.line})
	}
	return handler{ref: outRef{Module: "proxy", Params: params}}, extra, true
}

// proxyHeaders converts header_up or header_down lines as headerOps does.
func (c *converter) proxyHeaders(name string, ops [][]string, lines []int) map[string]any {
	var block []*directive
	for i, o := range ops {
		if len(o) > 0 {
			block = append(block, &directive{name: o[0], args: o[1:], line: lines[i]})
		}
	}
	return c.headerOps(&directive{name: name, line: lines[0]}, nil, block)
}

// upstream makes the upstream of reverse_proxy's or forward_auth's
// addresses and subdirectives, or reuses one made alike. other takes the
// subdirectives that are not the upstream's, and reports whether it did.
func (c *converter) upstream(d *directive, addrs []string, other func(*outUpstream, *directive) bool) (*outUpstream, bool) {
	if len(addrs) == 0 {
		c.skip(d, "no upstream address")
		return nil, false
	}
	u := &outUpstream{line: d.line}
	for _, a := range addrs {
		hosts, proto, why := upstreamAddr(a)
		if why != "" {
			c.warn(d.line, "%s %s: skipped: %s", d.name, a, why)
			continue
		}
		if len(u.Addresses) > 0 && proto != u.Protocol {
			c.warn(d.line, "%s %s: skipped: fit reaches an upstream's addresses alike", d.name, a)
			continue
		}
		u.Protocol = proto
		for _, h := range hosts {
			if !slices.Contains(u.Addresses, h) {
				u.Addresses = append(u.Addresses, h)
			}
		}
	}
	if len(u.Addresses) == 0 {
		c.skip(d, "none of its addresses converted")
		return nil, false
	}
	health := &outHealth{}
	for _, e := range d.block {
		if e.name == "to" {
			continue
		}
		one := len(e.args) == 1
		arg := ""
		if one {
			arg = e.args[0]
		}
		dur := func(set *string) {
			if t, ok := duration(arg); one && ok {
				*set = t
				return
			}
			c.skip(e, "it takes a duration")
		}
		num := func() (int, bool) {
			n, err := strconv.Atoi(arg)
			if !one || err != nil || n < 0 {
				c.skip(e, "it takes a number")
				return 0, false
			}
			return n, true
		}
		switch e.name {
		case "lb_policy":
			c.lbPolicy(u, e)
		case "health_uri", "health_path":
			if one {
				health.Path = arg
			}
		case "health_port":
			if n, ok := num(); ok && n > 0 && n < 65536 {
				health.Port = n
			}
		case "health_interval":
			dur(&health.Interval)
		case "health_timeout":
			dur(&health.Timeout)
		case "health_status":
			if n, err := strconv.Atoi(arg); one && err == nil && n >= 100 && n < 600 {
				health.ExpectStatus = []int{n}
			} else if arg != "2xx" {
				c.warn(e.line, "health_status %s: skipped: fit passes any status from 200 to 399, or those listed", arg)
			}
		case "health_passes":
			if n, ok := num(); ok {
				health.Passes = n
			}
		case "health_fails":
			if n, ok := num(); ok {
				health.Fails = n
			}
		case "health_headers":
			for _, f := range e.block {
				if strings.EqualFold(f.name, "host") && len(f.args) == 1 {
					health.Host = f.args[0]
				} else {
					c.skip(f, "fit's probes send a Host of their own and no other field")
				}
			}
		case "fail_duration":
			dur(&u.FailTimeout)
		case "max_fails":
			if n, ok := num(); ok {
				u.MaxFails = &n
			}
		case "unhealthy_status", "unhealthy_latency", "unhealthy_request_count", "health_body", "health_method", "health_request_body", "health_follow_redirects", "passive_health":
			c.skip(e, "not converted: fit counts failed connections and responses cut short")
		case "transport":
			c.transport(u, e)
		default:
			if other == nil || !other(u, e) {
				c.skip(e, "not converted")
			}
		}
	}
	if h := *health; h.ExpectStatus != nil || h.Path != "" || h.Host != "" || h.Port != 0 || h.Interval != "" || h.Timeout != "" || h.Fails != 0 || h.Passes != 0 {
		if health.Path == "" {
			health.Path = "/"
		}
		u.HealthCheck = health
	}
	return c.addUpstream(u), true
}

// addUpstream adds u, or returns the upstream made alike before it.
func (c *converter) addUpstream(u *outUpstream) *outUpstream {
	sig, _ := json.Marshal(u)
	if have, ok := c.upstreams[string(sig)]; ok {
		return have
	}
	base := "proxy_" + strings.NewReplacer(".", "_", ":", "_", "[", "", "]", "", "/", "_").Replace(u.Addresses[0])
	name := base
	for i := 2; slices.ContainsFunc(c.out.Upstreams, func(o *outUpstream) bool { return o.Name == name }); i++ {
		name = base + "-" + strconv.Itoa(i)
	}
	u.Name = name
	c.upstreams[string(sig)] = u
	c.out.Upstreams = append(c.out.Upstreams, u)
	return u
}

// upstreamAddr converts an upstream address of Caddy's, [scheme://]host[:port],
// and a port range as host:8001-8003, to fit's addresses and the protocol
// they are reached with.
func upstreamAddr(a string) ([]string, string, string) {
	if strings.Contains(a, "{") {
		return nil, "", "placeholders are not converted"
	}
	proto, port := "", "80"
	if scheme, rest, ok := strings.Cut(a, "://"); ok {
		switch scheme {
		case "http":
		case "https":
			proto, port = "h2", "443"
		case "h2c":
			proto = "h2c"
		default:
			return nil, "", "fit reaches upstreams over http://, https:// and h2c://"
		}
		a = rest
	}
	if strings.HasPrefix(a, "unix/") {
		return nil, "", "fit reaches upstreams over TCP"
	}
	if strings.Contains(a, "/") {
		return nil, "", "an upstream address has no path"
	}
	host, ports := a, port
	if h, p, err := net.SplitHostPort(a); err == nil {
		host, ports = h, p
	} else if strings.HasPrefix(a, ":") {
		host, ports = "", a[1:]
	}
	if host == "" {
		host = "localhost"
	}
	from, to := ports, ports
	if f, t, ok := strings.Cut(ports, "-"); ok {
		from, to = f, t
	}
	lo, err1 := strconv.Atoi(from)
	hi, err2 := strconv.Atoi(to)
	if err1 != nil || err2 != nil || lo < 1 || hi > 65535 || lo > hi || hi-lo > 100 {
		return nil, "", "not a port, or a range of 100 at most"
	}
	var out []string
	for p := lo; p <= hi; p++ {
		out = append(out, net.JoinHostPort(host, strconv.Itoa(p)))
	}
	return out, proto, ""
}

func (c *converter) lbPolicy(u *outUpstream, e *directive) {
	if len(e.args) == 0 {
		c.skip(e, "lb_policy names a policy")
		return
	}
	switch p := e.args[0]; p {
	case "round_robin", "least_conn", "random", "ip_hash", "uri_hash":
		u.LoadBalancing = p
	case "client_ip_hash":
		u.LoadBalancing = "ip_hash"
	case "random_choose":
		u.LoadBalancing = "p2c"
	case "header":
		if len(e.args) == 2 {
			u.LoadBalancing, u.HashKey = "header_hash", e.args[1]
			return
		}
		c.skip(e, "lb_policy header takes a field")
	case "cookie":
		name := "lb"
		if len(e.args) >= 2 {
			name = e.args[1]
		}
		u.StickyCookie = &outSticky{Name: name}
		if len(e.args) == 3 {
			c.warn(e.line, "lb_policy cookie: its secret is not converted: fit's cookie names the address")
		}
	case "weighted_round_robin":
		var ws []int
		for _, a := range e.args[1:] {
			n, err := strconv.Atoi(a)
			if err != nil || n < 1 {
				c.skip(e, "weights are numbers from 1")
				return
			}
			ws = append(ws, min(n, 1000))
		}
		if len(ws) != len(u.Addresses) {
			c.skip(e, "it needs a weight for each address")
			return
		}
		u.LoadBalancing, u.Weights = p, ws
	case "first":
		c.warn(e.line, "lb_policy first: written as round_robin: fit's proxy fails over to another upstream, named as failover, written by hand")
	default:
		c.skip(e, "fit balances by round robin, weights, least connections, random, P2C, or a hash of the client's address, the URI, a header or a cookie")
	}
}

func (c *converter) transport(u *outUpstream, e *directive) {
	if len(e.args) == 0 || e.args[0] != "http" {
		c.skip(e, "fit's proxy speaks HTTP; write php_fastcgi for FastCGI")
		return
	}
	for _, f := range e.block {
		one := len(f.args) == 1
		arg := ""
		if one {
			arg = f.args[0]
		}
		dur := func(set *string) {
			if t, ok := duration(arg); one && ok {
				*set = t
				return
			}
			c.skip(f, "it takes a duration")
		}
		switch f.name {
		case "versions":
			switch {
			case slices.Contains(f.args, "h2c"):
				u.Protocol = "h2c"
			case slices.Contains(f.args, "2") && u.Protocol != "h2":
				c.warn(f.line, "versions 2: fit speaks HTTP/2 to TLS upstreams, and h2c in the clear")
			}
		case "tls":
			u.Protocol = "h2"
		case "tls_server_name":
			if one {
				u.TLSServerName = arg
			}
		case "tls_trusted_ca_certs":
			if one {
				u.TLSCAFile = arg
			}
		case "tls_trust_pool":
			if len(f.args) == 2 && f.args[0] == "file" {
				u.TLSCAFile = f.args[1]
			} else {
				c.skip(f, "fit takes one CA file")
			}
		case "tls_client_auth":
			if len(f.args) == 2 {
				u.TLSCertFile, u.TLSKeyFile = f.args[0], f.args[1]
			}
		case "dial_timeout":
			dur(&u.ConnectTimeout)
		case "response_header_timeout":
			dur(&u.ResponseHeaderTimeout)
		case "read_timeout":
			dur(&u.ResponseIdleTimeout)
		case "keepalive":
			if arg == "off" {
				n := 0
				u.MaxIdleConns = &n
			} else {
				dur(&u.IdleTimeout)
			}
		case "keepalive_idle_conns", "keepalive_idle_conns_per_host":
			if n, err := strconv.Atoi(arg); one && err == nil && n >= 0 {
				u.MaxIdleConns = &n
			}
		case "max_conns_per_host":
			if n, err := strconv.Atoi(arg); one && err == nil && n >= 0 {
				u.MaxConns = n
			}
		case "tls_insecure_skip_verify":
			c.skip(f, "fit verifies upstreams' certificates; give it their CA with tls_trusted_ca_certs")
		default:
			c.skip(f, "not converted")
		}
	}
}

var days = regexp.MustCompile(`(\d+(?:\.\d+)?)d`)

// duration converts a duration of Caddy's, Go's with days, to fit's.
func duration(s string) (string, bool) {
	out := days.ReplaceAllStringFunc(s, func(x string) string {
		n, _ := strconv.ParseFloat(x[:len(x)-1], 64)
		return strconv.FormatFloat(24*n, 'f', -1, 64) + "h"
	})
	d, err := time.ParseDuration(out)
	return out, err == nil && d >= 0
}

var sizeRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([kKmMgGtT]?)(i?)[bB]?$`)

// size converts a size of Caddy's, as 10MB or 1GiB, to bytes.
func size(s string) (int64, bool) {
	m := sizeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	base := 1000.0
	if m[3] != "" {
		base = 1024
	}
	exp := strings.Index("kmgt", strings.ToLower(m[2])) + 1
	if m[2] == "" {
		exp = 0
	}
	v := n * math.Pow(base, float64(exp))
	if v > math.MaxInt64/2 {
		return 0, false
	}
	return int64(v), true
}
