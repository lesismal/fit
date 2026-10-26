// Package nginx converts a common subset of nginx configs to fit's native
// JSON: the http block's servers (listen, server_name, ssl_certificate),
// their locations (exact, prefix, ^~ and regular expressions, nested), and
// in them proxy_pass, upstream blocks, root, alias, index, try_files,
// return, rewrite, limit_req with limit_req_zone, client_max_body_size,
// add_header, proxy_set_header with literal values, gzip, and error_log
// and access_log, to fit's log and access log. Every directive it does
// not convert is reported, with its line, as a warning; fit adapt lists
// them.
package nginx

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/adapter"
)

func init() {
	adapter.Register("nginx", Convert)
}

// The native config the converter writes, its fields in the order the
// native config has them.
type (
	outConfig struct {
		Listeners []*outListener `json:"listeners"`
		Upstreams []*outUpstream `json:"upstreams,omitempty"`
		Routes    []*outRoute    `json:"routes"`
		Log       *outLog        `json:"log,omitempty"`
		AccessLog *outAccessLog  `json:"access_log,omitempty"`
	}
	outLog struct {
		Level  string `json:"level,omitempty"`
		Output string `json:"output,omitempty"`
	}
	outAccessLog struct {
		Output        string `json:"output,omitempty"`
		FlushInterval string `json:"flush_interval,omitempty"`
		BufferBytes   int64  `json:"buffer_bytes,omitempty"`
	}
	outListener struct {
		Name      string   `json:"name"`
		Address   string   `json:"address"`
		Protocols []string `json:"protocols,omitempty"`
		TLS       *outTLS  `json:"tls,omitempty"`
		line      int
	}
	outTLS struct {
		Certificates []outCert `json:"certificates"`
	}
	outCert struct {
		CertFile string `json:"cert_file"`
		KeyFile  string `json:"key_file"`
	}
	outUpstream struct {
		Name                string   `json:"name"`
		Addresses           []string `json:"addresses"`
		Weights             []int    `json:"weights,omitempty"`
		LoadBalancing       string   `json:"load_balancing,omitempty"`
		HashKey             string   `json:"hash_key,omitempty"`
		MaxConns            int      `json:"max_conns,omitempty"`
		MaxIdleConns        *int     `json:"max_idle_conns,omitempty"`
		ConnectTimeout      string   `json:"connect_timeout,omitempty"`
		Protocol            string   `json:"protocol,omitempty"`
		MaxFails            *int     `json:"max_fails,omitempty"`
		FailTimeout         string   `json:"fail_timeout,omitempty"`
		ResponseIdleTimeout string   `json:"response_idle_timeout,omitempty"`
		line                int
		// backup is the upstream of the servers marked backup, for proxies to
		// fail over to.
		backup string
	}
	outRoute struct {
		Name       string    `json:"name,omitempty"`
		Listeners  []string  `json:"listeners,omitempty"`
		Priority   int       `json:"priority,omitempty"`
		Match      *outMatch `json:"match,omitempty"`
		Middleware []outRef  `json:"middleware,omitempty"`
		Handler    outRef    `json:"handler"`
		line       int
	}
	outMatch struct {
		Host       []string `json:"host,omitempty"`
		Path       []string `json:"path,omitempty"`
		PathRegexp []string `json:"path_regexp,omitempty"`
	}
	outRef struct {
		Module string         `json:"module"`
		Params map[string]any `json:"params,omitempty"`
	}
)

// converter holds what the conversion has made so far.
type converter struct {
	out       outConfig
	warnings  []string
	listeners map[string]*outListener
	upstreams map[string]*outUpstream
	zones     map[string]zone
	servers   int
	names     map[string]bool
	// logLine and accessLogLine are the lines of the error_log and the
	// access_log converted, 0 before there is one.
	logLine, accessLogLine int
}

// routeName returns a route name, unique among the routes, made of the
// server's name and the location's path, with what route names may not
// hold replaced.
func (c *converter) routeName(server, path string) string {
	var b strings.Builder
	for _, r := range server + "-" + strings.Trim(path, "/") {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := strings.TrimRight(strings.TrimLeft(b.String(), "._-"), "_-")
	if name == "" {
		name = "route"
	}
	if len(name) > 60 {
		name = name[:60]
	}
	unique := name
	for i := 2; c.names[unique]; i++ {
		unique = name + "-" + strconv.Itoa(i)
	}
	c.names[unique] = true
	return unique
}

// zone is a limit_req_zone: what requests are counted by, and the rate.
type zone struct {
	key  string
	rate float64
}

// settings are what a context's directives set, as nginx inherits them:
// from http to server to location, and for add_header, limit_req and
// proxy_set_header only when the inner context sets none of its own.
type settings struct {
	root, alias  string
	index        []string
	tryFiles     []string
	bodyLimit    int64
	gzip         bool
	addHeaders   [][2]string
	limitReqs    []map[string]any
	proxyHeaders [][2]string
	hostHeader   string
	// set records which of the not-inherited-if-set lists this context set.
	setAddHeaders, setLimitReqs, setProxyHeaders bool
}

// Convert converts an nginx config to fit's native JSON.
func Convert(data []byte) (*adapter.Result, error) {
	ds, err := parse(data)
	if err != nil {
		return nil, err
	}
	c := &converter{listeners: map[string]*outListener{}, upstreams: map[string]*outUpstream{}, zones: map[string]zone{}, names: map[string]bool{}}
	for _, d := range ds {
		switch d.name {
		case "http":
			c.http(d)
		case "error_log":
			c.errorLog(d, "main")
		case "events", "user", "worker_processes", "worker_rlimit_nofile", "worker_cpu_affinity", "pid",
			"load_module", "daemon", "master_process", "thread_pool", "env", "worker_shutdown_timeout":
			c.skip(d, "fit runs as one process with its own workers")
		case "stream":
			c.skip(d, "TCP and UDP proxying is fit's tcp and udp listeners, written by hand")
		default:
			c.skip(d, "not converted")
		}
	}
	if len(c.out.Routes) == 0 {
		return nil, fmt.Errorf("the config has no server to convert")
	}
	// Warnings in the order of the config's lines.
	slices.SortStableFunc(c.warnings, func(a, b string) int { return warningLine(a) - warningLine(b) })
	out, err := json.Marshal(&c.out)
	if err != nil {
		return nil, err
	}
	pos := map[string]int{}
	for i, l := range c.out.Listeners {
		pos[fmt.Sprintf("listeners[%d]", i)] = l.line
	}
	for i, u := range c.out.Upstreams {
		pos[fmt.Sprintf("upstreams[%d]", i)] = u.line
	}
	for i, r := range c.out.Routes {
		pos[fmt.Sprintf("routes[%d]", i)] = r.line
	}
	if c.logLine > 0 {
		pos["log"] = c.logLine
	}
	if c.accessLogLine > 0 {
		pos["access_log"] = c.accessLogLine
	}
	return &adapter.Result{JSON: out, Warnings: c.warnings, Position: func(path string) (int, int, bool) {
		line, ok := pos[path]
		return line, 1, ok
	}}, nil
}

// warningLine returns the line a warning is about.
func warningLine(w string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(strings.SplitN(w, ":", 2)[0], "line "))
	return n
}

func (c *converter) warn(line int, format string, args ...any) {
	c.warnings = append(c.warnings, fmt.Sprintf("line %d: %s", line, fmt.Sprintf(format, args...)))
}

// skip reports that d is not converted, and why.
func (c *converter) skip(d *directive, why string) {
	c.warn(d.line, "%s: skipped: %s", d.name, why)
}

func (c *converter) http(h *directive) {
	base := &settings{bodyLimit: -1}
	// Upstreams and zones first, which servers refer to wherever they are.
	for _, d := range h.block {
		switch d.name {
		case "upstream":
			c.upstream(d)
		case "limit_req_zone":
			c.limitReqZone(d)
		}
	}
	for _, d := range h.block {
		switch d.name {
		case "upstream", "limit_req_zone", "server":
		default:
			c.setting(base, d, "http")
		}
	}
	for _, d := range h.block {
		if d.name == "server" {
			c.server(d, base)
		}
	}
}

// setting reads a directive that sets something servers and locations
// inherit, into s, and reports the rest as skipped.
func (c *converter) setting(s *settings, d *directive, where string) {
	switch d.name {
	case "root":
		if len(d.args) == 1 {
			s.root, s.alias = d.args[0], ""
			return
		}
	case "alias":
		if where == "location" && len(d.args) == 1 {
			s.alias = d.args[0]
			return
		}
	case "index":
		if len(d.args) > 0 {
			s.index = d.args
			return
		}
	case "try_files":
		if t, ok := c.tryFiles(d); ok {
			s.tryFiles = t
		}
		return
	case "client_max_body_size":
		if len(d.args) == 1 {
			if n, ok := size(d.args[0]); ok {
				s.bodyLimit = n
				return
			}
		}
	case "gzip":
		if len(d.args) == 1 {
			s.gzip = d.args[0] == "on"
			return
		}
	case "add_header":
		if len(d.args) >= 2 && len(d.args) <= 3 {
			if strings.Contains(d.args[1], "$") {
				c.skip(d, "its value has variables, which fit does not expand")
				return
			}
			if !s.setAddHeaders {
				s.addHeaders, s.setAddHeaders = nil, true
			}
			s.addHeaders = append(s.addHeaders, [2]string{d.args[0], d.args[1]})
			return
		}
	case "limit_req":
		if r, ok := c.limitReq(d); ok {
			if !s.setLimitReqs {
				s.limitReqs, s.setLimitReqs = nil, true
			}
			s.limitReqs = append(s.limitReqs, r)
		}
		return
	case "proxy_set_header":
		c.proxySetHeader(s, d)
		return
	case "proxy_http_version", "proxy_redirect":
		// fit's proxy speaks HTTP/1.1 to upstreams and keeps their Location.
		return
	case "gzip_types", "gzip_comp_level", "gzip_min_length", "gzip_vary", "gzip_proxied", "gzip_http_version", "gzip_disable", "gzip_buffers":
		c.skip(d, "fit's compress middleware compresses what compresses well, with zstd, brotli or gzip")
		return
	case "ssl_protocols", "ssl_ciphers", "ssl_prefer_server_ciphers", "ssl_session_cache", "ssl_session_timeout",
		"ssl_session_tickets", "ssl_ecdh_curve", "ssl_dhparam", "ssl_stapling", "ssl_stapling_verify":
		c.skip(d, "fit picks TLS versions and ciphers, and staples OCSP, itself")
		return
	case "error_log":
		c.errorLog(d, where)
		return
	case "access_log":
		c.accessLog(d, where)
		return
	case "log_format":
		c.skip(d, "fit writes its access log lines as JSON, with fields of its own")
		return
	case "sendfile", "tcp_nopush", "tcp_nodelay", "keepalive_timeout", "keepalive_requests", "types_hash_max_size",
		"server_tokens", "default_type", "include", "types", "charset", "client_body_buffer_size",
		"client_header_buffer_size", "large_client_header_buffers", "proxy_buffering", "proxy_buffers",
		"proxy_buffer_size", "proxy_busy_buffers_size", "proxy_send_timeout", "send_timeout",
		"client_body_timeout", "client_header_timeout", "resolver", "resolver_timeout":
		c.skip(d, "not converted: fit's defaults stand in for it")
		return
	}
	c.skip(d, "not converted")
}

// inherit returns a location's settings, starting from its parent's.
func inherit(p *settings) *settings {
	s := *p
	s.setAddHeaders, s.setLimitReqs, s.setProxyHeaders = false, false, false
	s.addHeaders = slices.Clone(p.addHeaders)
	s.limitReqs = slices.Clone(p.limitReqs)
	s.proxyHeaders = slices.Clone(p.proxyHeaders)
	return &s
}

// serverInfo is what a server's locations share.
type serverInfo struct {
	name      string
	hosts     []string
	listeners []string
	rewrites  []outRef
	vars      map[string]string
}

func (c *converter) server(sd *directive, base *settings) {
	c.servers++
	s := inherit(base)
	info := &serverInfo{name: fmt.Sprintf("server%d", c.servers), vars: map[string]string{}}
	var listens []*directive
	var certs []outCert
	var cert, key string
	var locations []*directive
	var ret *directive
	http2 := false
	for _, d := range sd.block {
		switch d.name {
		case "listen":
			listens = append(listens, d)
		case "server_name":
			c.serverName(info, d)
		case "ssl_certificate":
			if len(d.args) == 1 {
				cert = d.args[0]
			}
		case "ssl_certificate_key":
			if len(d.args) == 1 {
				key = d.args[0]
			}
		case "http2":
			http2 = len(d.args) == 1 && d.args[0] == "on"
		case "location":
			locations = append(locations, d)
		case "return":
			ret = d
		case "rewrite":
			if r, ok := c.rewrite(d, info); ok {
				info.rewrites = append(info.rewrites, r)
			}
		case "ssl":
			c.skip(d, "write ssl on listen")
		default:
			c.setting(s, d, "server")
		}
	}
	if cert != "" && key != "" {
		certs = append(certs, outCert{cert, key})
	}
	if len(listens) == 0 {
		listens = []*directive{{name: "listen", args: []string{"80"}, line: sd.line}}
	}
	for _, d := range listens {
		if name, ok := c.listen(d, certs, http2); ok && !slices.Contains(info.listeners, name) {
			info.listeners = append(info.listeners, name)
		}
	}
	if len(info.listeners) == 0 {
		c.warn(sd.line, "server: skipped: none of its listen directives converted")
		return
	}
	if ret != nil {
		// A server's return answers every request before any location is
		// looked at.
		h, mw, ok := c.ret(ret, info)
		if !ok {
			return
		}
		for _, l := range locations {
			c.warn(l.line, "location %s: skipped: the server's return answers every request first", strings.Join(l.args, " "))
		}
		c.out.Routes = append(c.out.Routes, &outRoute{
			Name: c.routeName(info.name, ""), Listeners: info.listeners, Match: hostMatch(info.hosts),
			Middleware: append(slices.Clone(info.rewrites), mw...), Handler: h, line: ret.line,
		})
		return
	}
	hasRoot := false
	regexps := 0
	for _, l := range locations {
		c.location(l, s, info, "", &regexps)
		if len(l.args) == 1 && l.args[0] == "/" || len(l.args) == 2 && l.args[0] == "^~" && l.args[1] == "/" {
			hasRoot = true
		}
	}
	if !hasRoot {
		// What no location takes, the server answers as nginx does: from
		// its root, or with 404.
		c.addRoute(sd, s, info, &outMatch{Host: info.hosts}, -1, c.routeName(info.name, "default"), "")
	}
}

func hostMatch(hosts []string) *outMatch {
	if len(hosts) == 0 {
		return nil
	}
	return &outMatch{Host: hosts}
}

func (c *converter) serverName(info *serverInfo, d *directive) {
	for _, n := range d.args {
		switch {
		case n == "_" || n == "" || n == "localhost" && len(d.args) == 1:
			// A catch-all: no host to match.
		case strings.HasPrefix(n, "~"):
			c.warn(d.line, "server_name %s: skipped: a regular expression of names is not converted; the server matches its other names", n)
		case strings.HasPrefix(n, "*."):
			info.hosts = append(info.hosts, "**"+n[1:])
		case strings.HasPrefix(n, "."):
			info.hosts = append(info.hosts, n[1:], "**"+n)
		case strings.HasSuffix(n, ".*"):
			c.warn(d.line, "server_name %s: skipped: a wildcard at the end of a name is not converted", n)
		default:
			info.hosts = append(info.hosts, strings.ToLower(n))
		}
		if len(info.hosts) > 0 && info.vars["$server_name"] == "" {
			info.vars["$server_name"] = info.hosts[0]
		}
	}
	if len(d.args) > 0 && !strings.HasPrefix(d.args[0], "~") && d.args[0] != "_" {
		info.name = d.args[0]
	}
}

// listen converts a listen directive to a listener, shared with the other
// servers on the same address, and returns its name.
func (c *converter) listen(d *directive, certs []outCert, http2 bool) (string, bool) {
	if len(d.args) == 0 {
		c.skip(d, "no address")
		return "", false
	}
	addr := d.args[0]
	if strings.HasPrefix(addr, "unix:") {
		c.skip(d, "fit listens on TCP and UDP addresses")
		return "", false
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		if _, err := strconv.Atoi(addr); err == nil {
			host, port = "", addr
		} else {
			host, port = addr, "80"
		}
	}
	if host == "*" || host == "::" || host == "0.0.0.0" {
		// fit's ":port" takes every address, IPv4 and IPv6.
		host = ""
	}
	tls, quic := false, false
	for _, a := range d.args[1:] {
		switch {
		case a == "ssl":
			tls = true
		case a == "http2":
			http2 = true
		case a == "quic" || a == "http3":
			quic, tls = true, true
		case a == "default_server" || a == "default" || a == "reuseport" || a == "ipv6only=on" || a == "ipv6only=off" ||
			strings.HasPrefix(a, "backlog=") || strings.HasPrefix(a, "rcvbuf=") || strings.HasPrefix(a, "sndbuf=") || a == "deferred":
		case a == "proxy_protocol":
			c.warn(d.line, "listen %s proxy_protocol: the PROXY protocol is not converted: set the listener's proxy_protocol and the proxies it trusts", addr)
		default:
			c.warn(d.line, "listen %s %s: skipped: not converted", addr, a)
		}
	}
	if tls && len(certs) == 0 {
		c.warn(d.line, "listen %s: skipped: ssl with no ssl_certificate and ssl_certificate_key in its server", addr)
		return "", false
	}
	address := net.JoinHostPort(host, port)
	if host == "" {
		address = ":" + port
	}
	l := c.listeners[address]
	if l == nil {
		scheme := "http"
		if tls {
			scheme = "https"
		}
		name := scheme + "_" + strings.NewReplacer(".", "_", ":", "_", "[", "", "]", "").Replace(strings.TrimPrefix(address, ":"))
		l = &outListener{Name: name, Address: address, Protocols: []string{"http/1.1"}, line: d.line}
		if tls {
			l.TLS = &outTLS{}
		}
		c.listeners[address] = l
		c.out.Listeners = append(c.out.Listeners, l)
	} else if (l.TLS != nil) != tls && !quic {
		c.warn(d.line, "listen %s: skipped: another server listens there with%s ssl", addr, map[bool]string{true: "out", false: ""}[tls])
		return "", false
	}
	if tls {
		for _, cert := range certs {
			if !slices.Contains(l.TLS.Certificates, cert) {
				l.TLS.Certificates = append(l.TLS.Certificates, cert)
			}
		}
	}
	add := func(p string) {
		if !slices.Contains(l.Protocols, p) {
			l.Protocols = append(l.Protocols, p)
		}
	}
	if http2 {
		if tls {
			add("h2")
		} else {
			add("h2c")
		}
	}
	if quic {
		add("h3")
	}
	return l.Name, true
}

// location converts a location block, and those nested in it, to routes.
func (c *converter) location(d *directive, parent *settings, info *serverInfo, outer string, regexps *int) {
	if len(d.args) == 0 || len(d.args) > 2 {
		c.skip(d, "a location needs a path")
		return
	}
	mod, p := "", d.args[0]
	if len(d.args) == 2 {
		mod, p = d.args[0], d.args[1]
	}
	if strings.HasPrefix(p, "@") {
		c.skip(d, "named locations, for try_files and error_page, are not converted")
		return
	}
	m := &outMatch{Host: info.hosts}
	priority := 0
	switch mod {
	case "=":
		m.Path, priority = []string{p}, 30000
	case "", "^~":
		m.Path = []string{p + "*"}
		if mod == "^~" {
			priority = 20000
		}
	case "~", "~*":
		re := p
		if mod == "~*" {
			re = "(?i)" + re
		}
		if _, err := regexp.Compile(re); err != nil {
			c.warn(d.line, "location %s %s: skipped: Go's regular expressions do not take it: %v", mod, p, err)
			return
		}
		m.PathRegexp = []string{re}
		// nginx tries regular expressions in order: the first wins.
		*regexps++
		priority = 10000 - *regexps
	default:
		c.skip(d, "an unknown location modifier "+mod)
		return
	}
	s := inherit(parent)
	var nested []*directive
	var ret *directive
	var proxy *directive
	var rewrites []outRef
	for _, e := range d.block {
		switch e.name {
		case "location":
			nested = append(nested, e)
		case "return":
			ret = e
		case "proxy_pass":
			proxy = e
		case "rewrite":
			if r, ok := c.rewrite(e, info); ok {
				rewrites = append(rewrites, r)
			}
		case "proxy_connect_timeout", "proxy_read_timeout":
			// Kept for the upstream, below.
		case "if":
			c.skip(e, "if blocks are not converted")
		default:
			c.setting(s, e, "location")
		}
	}
	r := &outRoute{Name: c.routeName(info.name, p), Listeners: info.listeners, Priority: priority, Match: m, line: d.line}
	r.Middleware = append(slices.Clone(info.rewrites), rewrites...)
	r.Middleware = append(r.Middleware, c.limits(s)...)
	switch {
	case ret != nil:
		h, mw, ok := c.ret(ret, info)
		if !ok {
			return
		}
		r.Middleware = append(r.Middleware, mw...)
		r.Handler = h
	case proxy != nil:
		h, mw, ok := c.proxyPass(proxy, d, mod, p, s)
		if !ok {
			return
		}
		r.Middleware = append(r.Middleware, mw...)
		r.Handler = h
	default:
		h, ok := c.static(d, s, mod, p)
		if !ok {
			return
		}
		r.Handler = h
	}
	r.Middleware = append(r.Middleware, c.response(s)...)
	c.out.Routes = append(c.out.Routes, r)
	for _, n := range nested {
		c.location(n, s, info, p, regexps)
	}
}

// addRoute adds a route for a server's requests no location takes.
func (c *converter) addRoute(d *directive, s *settings, info *serverInfo, m *outMatch, priority int, name, prefix string) {
	h, ok := c.static(d, s, "", prefix)
	if !ok {
		return
	}
	if m != nil && len(m.Host) == 0 && len(m.Path) == 0 {
		m = nil
	}
	c.out.Routes = append(c.out.Routes, &outRoute{
		Name: name, Listeners: info.listeners, Priority: priority, Match: m,
		Middleware: append(append(slices.Clone(info.rewrites), c.limits(s)...), c.response(s)...), Handler: h, line: d.line,
	})
}

// limits returns the middleware of a context's limits, which nginx applies
// after its rewrites and before the content.
func (c *converter) limits(s *settings) []outRef {
	var mw []outRef
	for _, r := range s.limitReqs {
		mw = append(mw, outRef{Module: "rate_limit", Params: r})
	}
	if s.bodyLimit > 0 {
		mw = append(mw, outRef{Module: "body_limit", Params: map[string]any{"max_bytes": s.bodyLimit}})
	}
	return mw
}

// response returns the middleware that changes a context's responses: the
// fields add_header adds, and compression.
func (c *converter) response(s *settings) []outRef {
	var mw []outRef
	if len(s.addHeaders) > 0 {
		add := map[string][]string{}
		for _, h := range s.addHeaders {
			add[h[0]] = append(add[h[0]], h[1])
		}
		mw = append(mw, outRef{Module: "headers", Params: map[string]any{"response": map[string]any{"add": add}}})
	}
	if s.gzip {
		mw = append(mw, outRef{Module: "compress"})
	}
	return mw
}

// static makes the handler of a location that serves files, or answers
// 404 when the location says nothing of what to serve.
func (c *converter) static(d *directive, s *settings, mod, prefix string) (outRef, bool) {
	if s.root == "" && s.alias == "" {
		if len(s.tryFiles) > 0 {
			c.warn(d.line, "%s %s: try_files with no root: answered 404", d.name, strings.Join(d.args, " "))
		}
		return outRef{Module: "respond", Params: map[string]any{"status": 404}}, true
	}
	params := map[string]any{}
	if s.alias != "" {
		if mod != "" && mod != "^~" {
			c.warn(d.line, "location %s %s: skipped: alias in a location of a regular expression is not converted", mod, prefix)
			return outRef{}, false
		}
		params["root"] = strings.TrimSuffix(s.alias, "/")
		if strings.TrimSuffix(prefix, "/") != "" {
			params["strip_prefix"] = strings.TrimSuffix(prefix, "/")
		}
	} else {
		params["root"] = s.root
	}
	if len(s.index) > 0 && !slices.Equal(s.index, []string{"index.html"}) {
		params["index"] = s.index
	}
	if len(s.tryFiles) > 0 {
		params["try_files"] = s.tryFiles
	}
	return outRef{Module: "static", Params: params}, true
}

// tryFiles converts try_files, its variables to fit's placeholders.
func (c *converter) tryFiles(d *directive) ([]string, bool) {
	var out []string
	for i, a := range d.args {
		if strings.HasPrefix(a, "=") {
			if i != len(d.args)-1 {
				break
			}
			if a != "=404" {
				c.warn(d.line, "try_files: the last %s answers 404 in fit", a)
			}
			continue
		}
		if strings.HasPrefix(a, "@") {
			c.skip(d, "named locations are not converted")
			return nil, false
		}
		t := strings.ReplaceAll(strings.ReplaceAll(a, "$uri", "{path}"), "$document_uri", "{path}")
		if strings.Contains(t, "$") {
			c.skip(d, "it has variables other than $uri")
			return nil, false
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		c.skip(d, "nothing to try")
		return nil, false
	}
	return out, true
}

// vars replaces the nginx variables fit has placeholders for; it reports
// false for any other.
func vars(s string, info *serverInfo) (string, bool) {
	r := strings.NewReplacer(
		"$request_uri", "{uri}", "$scheme", "{scheme}", "$http_host", "{host}", "$host", "{host}",
		"$uri", "{path}", "$document_uri", "{path}", "$query_string", "{query}", "$args", "{query}",
		"$server_name", info.vars["$server_name"],
	)
	out := r.Replace(s)
	rest := regexp.MustCompile(`\$[A-Za-z_]\w*|\$\{[A-Za-z_]\w*\}`).FindString(out)
	return out, rest == ""
}

// ret converts return to a handler, and a redirect in front of it.
func (c *converter) ret(d *directive, info *serverInfo) (outRef, []outRef, bool) {
	if len(d.args) == 0 || len(d.args) > 2 {
		c.skip(d, "return takes a status and a text or URL")
		return outRef{}, nil, false
	}
	code, err := strconv.Atoi(d.args[0])
	if err != nil {
		// return URL: a temporary redirect.
		code, d = 302, &directive{name: d.name, args: []string{"302", d.args[0]}, line: d.line}
	}
	if code < 100 || code > 599 {
		c.skip(d, "a status out of range")
		return outRef{}, nil, false
	}
	if len(d.args) == 1 {
		return outRef{Module: "respond", Params: map[string]any{"status": code}}, nil, true
	}
	switch code {
	case 301, 302, 303, 307, 308:
		to, ok := vars(d.args[1], info)
		if !ok {
			c.skip(d, "its URL has variables fit has no placeholder for")
			return outRef{}, nil, false
		}
		return outRef{Module: "respond", Params: map[string]any{"status": 404}},
			[]outRef{{Module: "redirect", Params: map[string]any{"to": to, "status": code}}}, true
	}
	if strings.Contains(d.args[1], "$") {
		c.skip(d, "its text has variables, which fit does not expand")
		return outRef{}, nil, false
	}
	return outRef{Module: "respond", Params: map[string]any{"status": code, "body": d.args[1]}}, nil, true
}

// rewrite converts rewrite to the rewrite middleware, or to a redirect.
func (c *converter) rewrite(d *directive, info *serverInfo) (outRef, bool) {
	if len(d.args) < 2 || len(d.args) > 3 {
		c.skip(d, "rewrite takes a regular expression, a replacement and a flag")
		return outRef{}, false
	}
	re, to := d.args[0], d.args[1]
	if _, err := regexp.Compile(re); err != nil {
		c.warn(d.line, "rewrite: skipped: Go's regular expressions do not take %q: %v", re, err)
		return outRef{}, false
	}
	flag := ""
	if len(d.args) == 3 {
		flag = d.args[2]
	}
	if flag == "redirect" || flag == "permanent" || strings.HasPrefix(to, "http://") || strings.HasPrefix(to, "https://") || strings.HasPrefix(to, "$scheme") {
		status := 302
		if flag == "permanent" {
			status = 301
		}
		u, ok := vars(to, info)
		if !ok {
			c.skip(d, "its replacement has variables fit has no placeholder for")
			return outRef{}, false
		}
		return outRef{Module: "redirect", Params: map[string]any{"regexp": re, "to": u, "status": status}}, true
	}
	if strings.Contains(to, "?") || strings.Contains(to, "$") && regexp.MustCompile(`\$[A-Za-z_{]`).MatchString(to) {
		c.skip(d, "a replacement with a query or with variables is not converted")
		return outRef{}, false
	}
	if flag == "last" {
		c.warn(d.line, "rewrite ... last: converted, but fit does not look for the location again after it")
	}
	// nginx replaces the whole path when the expression matches anywhere
	// in it; fit's rewrite replaces each match, so the expression is made
	// to cover the whole path.
	whole := re
	if !strings.HasPrefix(re, "^") || !strings.HasSuffix(re, "$") {
		whole = "^.*?(?:" + strings.TrimSuffix(strings.TrimPrefix(re, "^"), "$") + ").*$"
		if strings.HasPrefix(re, "^") {
			whole = "^(?:" + strings.TrimPrefix(re, "^") + ").*$"
		}
	}
	return outRef{Module: "rewrite", Params: map[string]any{"regexp": whole, "replacement": to}}, true
}

func (c *converter) proxySetHeader(s *settings, d *directive) {
	if len(d.args) != 2 {
		c.skip(d, "proxy_set_header takes a field and a value")
		return
	}
	name, value := strings.ToLower(d.args[0]), d.args[1]
	switch {
	case name == "host" && (value == "$host" || value == "$http_host"):
		// fit's proxy sends the client's Host.
	case name == "host" && !strings.Contains(value, "$"):
		s.hostHeader = value
	case name == "connection" || name == "upgrade":
		// fit's proxy joins WebSockets and other upgrades itself.
	case (name == "x-forwarded-for" && value == "$proxy_add_x_forwarded_for") || (name == "x-forwarded-proto" && value == "$scheme") ||
		(name == "x-forwarded-host" && (value == "$host" || value == "$http_host")):
		// fit's proxy sets X-Forwarded-For, -Proto and -Host itself.
	case strings.Contains(value, "$"):
		c.skip(d, "its value has variables, which fit does not expand")
	default:
		if !s.setProxyHeaders {
			s.proxyHeaders, s.setProxyHeaders = nil, true
		}
		s.proxyHeaders = append(s.proxyHeaders, [2]string{d.args[0], value})
	}
}

// proxyPass converts proxy_pass to the proxy handler, with the upstream it
// names or one made for its address, and the rewrite its URI asks for.
func (c *converter) proxyPass(d, loc *directive, mod, prefix string, s *settings) (outRef, []outRef, bool) {
	if len(d.args) != 1 {
		c.skip(d, "proxy_pass takes one URL")
		return outRef{}, nil, false
	}
	target := d.args[0]
	if strings.Contains(target, "$") {
		c.skip(d, "a URL with variables is not converted")
		return outRef{}, nil, false
	}
	scheme, rest, ok := strings.Cut(target, "://")
	if !ok || scheme != "http" && scheme != "https" {
		c.skip(d, "fit proxies to http:// and https:// upstreams")
		return outRef{}, nil, false
	}
	hostport, uri, hasURI := strings.Cut(rest, "/")
	uri = "/" + uri
	u := c.upstreams[hostport]
	if u == nil {
		addr := hostport
		if _, _, err := net.SplitHostPort(addr); err != nil {
			port := "80"
			if scheme == "https" {
				port = "443"
			}
			addr = net.JoinHostPort(addr, port)
		}
		name := "proxy_" + strings.NewReplacer(".", "_", ":", "_", "[", "", "]", "").Replace(hostport)
		if u = c.upstreams[name]; u == nil {
			u = &outUpstream{Name: name, Addresses: []string{addr}, line: d.line}
			c.upstreams[name] = u
			c.out.Upstreams = append(c.out.Upstreams, u)
		}
	}
	if scheme == "https" && u.Protocol == "" {
		u.Protocol = "h2"
		c.warn(d.line, "proxy_pass %s: fit reaches TLS upstreams over HTTP/2", target)
	}
	for _, e := range loc.block {
		switch e.name {
		case "proxy_connect_timeout":
			if t, ok := duration(e.args); ok {
				u.ConnectTimeout = t
			}
		case "proxy_read_timeout":
			if t, ok := duration(e.args); ok {
				u.ResponseIdleTimeout = t
			}
		}
	}
	params := map[string]any{"upstream": u.Name}
	if u.backup != "" {
		params["failover"] = u.backup
	}
	if s.hostHeader != "" {
		params["host_header"] = s.hostHeader
	}
	var mw []outRef
	if len(s.proxyHeaders) > 0 {
		set := map[string]string{}
		for _, h := range s.proxyHeaders {
			set[h[0]] = h[1]
		}
		mw = append(mw, outRef{Module: "headers", Params: map[string]any{"request": map[string]any{"set": set}}})
	}
	if hasURI {
		if mod == "~" || mod == "~*" || mod == "=" {
			c.warn(d.line, "proxy_pass %s: the URI is not converted in a location of %s", target, mod)
		} else {
			// The location's prefix is replaced with the URI.
			rw := map[string]any{}
			if p := strings.TrimSuffix(prefix, "/"); p != "" {
				rw["strip_prefix"] = []string{p}
			}
			if a := strings.TrimSuffix(uri, "/"); a != "" {
				rw["add_prefix"] = a
			}
			if len(rw) > 0 {
				mw = append(mw, outRef{Module: "rewrite", Params: rw})
			}
		}
	}
	return outRef{Module: "proxy", Params: params}, mw, true
}

// upstream converts an upstream block. Servers marked backup go to an
// upstream of their own, which proxies to it fail over to.
func (c *converter) upstream(d *directive) {
	if len(d.args) != 1 {
		c.skip(d, "an upstream needs a name")
		return
	}
	u := &outUpstream{Name: d.args[0], line: d.line}
	var backup *outUpstream
	weighted := false
	var weights []int
	maxFails, failTimeout := "", ""
	for _, e := range d.block {
		switch e.name {
		case "server":
			if len(e.args) == 0 {
				c.skip(e, "no address")
				continue
			}
			addr := e.args[0]
			if strings.HasPrefix(addr, "unix:") {
				c.skip(e, "fit reaches upstreams over TCP")
				continue
			}
			if _, _, err := net.SplitHostPort(addr); err != nil {
				addr = net.JoinHostPort(addr, "80")
			}
			weight, isBackup, down := 1, false, false
			for _, a := range e.args[1:] {
				k, v, _ := strings.Cut(a, "=")
				switch k {
				case "weight":
					if n, err := strconv.Atoi(v); err == nil && n > 0 {
						weight, weighted = min(n, 1000), weighted || n != 1
					}
				case "max_fails":
					maxFails = v
				case "fail_timeout":
					failTimeout = v
				case "backup":
					isBackup = true
				case "down":
					down = true
				case "max_conns":
					if n, err := strconv.Atoi(v); err == nil && n > 0 {
						u.MaxConns = max(u.MaxConns, n)
					}
				default:
					c.warn(e.line, "server %s %s: skipped: not converted", e.args[0], a)
				}
			}
			switch {
			case down:
				c.warn(e.line, "server %s down: left out", e.args[0])
			case isBackup:
				if backup == nil {
					backup = &outUpstream{Name: u.Name + "_backup", line: e.line}
				}
				backup.Addresses = append(backup.Addresses, addr)
			default:
				u.Addresses = append(u.Addresses, addr)
				weights = append(weights, weight)
			}
		case "least_conn":
			u.LoadBalancing = "least_conn"
		case "ip_hash":
			u.LoadBalancing = "ip_hash"
		case "random":
			u.LoadBalancing = "random"
			if len(e.args) > 0 {
				u.LoadBalancing = "p2c"
			}
		case "hash":
			if len(e.args) == 0 {
				c.skip(e, "hash takes a key")
				continue
			}
			switch key := e.args[0]; {
			case key == "$request_uri" || key == "$uri":
				u.LoadBalancing = "uri_hash"
			case key == "$remote_addr" || key == "$binary_remote_addr":
				u.LoadBalancing = "ip_hash"
			case strings.HasPrefix(key, "$http_"):
				u.LoadBalancing, u.HashKey = "header_hash", strings.ReplaceAll(strings.TrimPrefix(key, "$http_"), "_", "-")
			case strings.HasPrefix(key, "$cookie_"):
				u.LoadBalancing, u.HashKey = "cookie_hash", strings.TrimPrefix(key, "$cookie_")
			default:
				c.warn(e.line, "hash %s: skipped: fit hashes on the client's address, the URI, a header or a cookie", key)
			}
		case "keepalive":
			if len(e.args) == 1 {
				if n, err := strconv.Atoi(e.args[0]); err == nil {
					u.MaxIdleConns = &n
					continue
				}
			}
			c.skip(e, "keepalive takes a number")
		case "keepalive_timeout", "keepalive_requests", "keepalive_time":
			c.skip(e, "fit's upstream idle_timeout stands in for it")
		default:
			c.skip(e, "not converted")
		}
	}
	if len(u.Addresses) == 0 {
		if backup == nil {
			c.warn(d.line, "upstream %s: skipped: no server converted", u.Name)
			return
		}
		u.Addresses, backup = backup.Addresses, nil
	}
	if weighted {
		u.Weights = weights
		if u.LoadBalancing == "" {
			u.LoadBalancing = "weighted_round_robin"
		}
	}
	if maxFails != "" {
		if n, err := strconv.Atoi(maxFails); err == nil {
			u.MaxFails = &n
		}
	}
	if t, ok := duration([]string{failTimeout}); ok && failTimeout != "" {
		u.FailTimeout = t
	}
	c.upstreams[u.Name] = u
	c.out.Upstreams = append(c.out.Upstreams, u)
	if backup != nil {
		u.backup = backup.Name
		c.upstreams[backup.Name] = backup
		c.out.Upstreams = append(c.out.Upstreams, backup)
	}
}

func (c *converter) limitReqZone(d *directive) {
	var key, name string
	var rate float64
	for i, a := range d.args {
		switch {
		case i == 0:
			switch {
			case a == "$binary_remote_addr" || a == "$remote_addr":
				key = "ip"
			case strings.HasPrefix(a, "$http_"):
				key = "header:" + strings.ReplaceAll(strings.TrimPrefix(a, "$http_"), "_", "-")
			case a == "$server_name" || a == "$host":
				key = "route"
			default:
				c.warn(d.line, "limit_req_zone %s: skipped: fit counts requests by the client's address, a header, or the route", a)
				return
			}
		case strings.HasPrefix(a, "zone="):
			name, _, _ = strings.Cut(strings.TrimPrefix(a, "zone="), ":")
		case strings.HasPrefix(a, "rate="):
			r := strings.TrimPrefix(a, "rate=")
			per := 1.0
			switch {
			case strings.HasSuffix(r, "r/s"):
				r = strings.TrimSuffix(r, "r/s")
			case strings.HasSuffix(r, "r/m"):
				r, per = strings.TrimSuffix(r, "r/m"), 60
			}
			n, err := strconv.ParseFloat(r, 64)
			if err != nil {
				c.skip(d, "a rate fit cannot read")
				return
			}
			rate = n / per
		}
	}
	if name == "" || rate <= 0 {
		c.skip(d, "a zone needs a name and a rate")
		return
	}
	c.zones[name] = zone{key, rate}
}

func (c *converter) limitReq(d *directive) (map[string]any, bool) {
	var z zone
	found := false
	params := map[string]any{}
	nodelay := false
	for _, a := range d.args {
		k, v, _ := strings.Cut(a, "=")
		switch k {
		case "zone":
			z, found = c.zones[v]
		case "burst":
			if n, err := strconv.Atoi(v); err == nil {
				params["burst"] = n
			}
		case "nodelay":
			nodelay = true
		case "delay":
		}
	}
	if !found {
		c.skip(d, "its zone was not converted")
		return nil, false
	}
	params["rate"] = z.rate
	params["key"] = z.key
	if !nodelay {
		c.warn(d.line, "limit_req without nodelay: nginx delays requests past the rate, fit refuses them with 429")
	}
	return params, true
}

// duration converts an nginx time, as 60s, 1m or 500ms, to a Go one.
func duration(args []string) (string, bool) {
	if len(args) != 1 || args[0] == "" {
		return "", false
	}
	s := args[0]
	if _, err := strconv.Atoi(s); err == nil {
		return s + "s", true
	}
	m := regexp.MustCompile(`^(\d+(ms|s|m|h|d|w))+$`).MatchString(s)
	if !m {
		return "", false
	}
	// Go has no days or weeks.
	return regexp.MustCompile(`(\d+)(d|w)`).ReplaceAllStringFunc(s, func(x string) string {
		n, _ := strconv.Atoi(x[:len(x)-1])
		if x[len(x)-1] == 'w' {
			n *= 7
		}
		return strconv.Itoa(24*n) + "h"
	}), true
}

// size converts an nginx size, as 10m, to bytes.
func size(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'k', 'K':
		mult, s = 1<<10, s[:len(s)-1]
	case 'm', 'M':
		mult, s = 1<<20, s[:len(s)-1]
	case 'g', 'G':
		mult, s = 1<<30, s[:len(s)-1]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n * mult, err == nil && n >= 0
}

// logLevels are fit's log levels for nginx's error_log levels; crit, alert
// and emerg are error, fit's highest.
var logLevels = map[string]string{"debug": "debug", "info": "info", "notice": "info", "warn": "warn", "error": "error",
	"crit": "error", "alert": "error", "emerg": "error"}

// logOutput returns fit's output for an nginx log's file: stdout or stderr
// for theirs, or the path. Syslog and memory buffers have none.
func logOutput(file string) (string, bool) {
	switch {
	case file == "stderr" || file == "/dev/stderr":
		return "stderr", true
	case file == "/dev/stdout":
		return "stdout", true
	case strings.HasPrefix(file, "syslog:") || strings.HasPrefix(file, "memory:"):
		return "", false
	}
	return file, true
}

// errorLog converts an error_log, from the context where, to fit's log,
// which is the whole process's: the first one converted is it.
func (c *converter) errorLog(d *directive, where string) {
	if len(d.args) == 0 || len(d.args) > 2 {
		c.skip(d, "not converted")
		return
	}
	output, ok := logOutput(d.args[0])
	if !ok {
		c.skip(d, "fit writes its log to stdout, stderr or a file")
		return
	}
	// nginx's level when none is named.
	level := "error"
	if len(d.args) == 2 {
		if level, ok = logLevels[d.args[1]]; !ok {
			c.skip(d, "not a level")
			return
		}
		if d.args[1] == "crit" || d.args[1] == "alert" || d.args[1] == "emerg" {
			c.warn(d.line, "error_log: %s is fit's error, its highest level", d.args[1])
		}
	}
	if l := c.out.Log; l != nil {
		if l.Output != output || l.Level != level {
			c.warn(d.line, "error_log: skipped: fit has one log, for the whole process, which line %d sets", c.logLine)
		}
		return
	}
	if where == "server" || where == "location" {
		c.warn(d.line, "error_log: fit has one log, for the whole process, not one for each %s", where)
	}
	c.out.Log = &outLog{Level: level, Output: output}
	c.logLine = d.line
}

// accessLog converts an access_log, from the context where, to fit's access
// log, which writes a line for every request: the first one converted is
// it.
func (c *converter) accessLog(d *directive, where string) {
	if len(d.args) == 1 && d.args[0] == "off" {
		if c.out.AccessLog != nil || where == "location" {
			c.warn(d.line, "access_log off: skipped: fit's access log, when there is one, writes a line for every request")
		}
		return
	}
	if len(d.args) == 0 {
		c.skip(d, "not converted")
		return
	}
	output, ok := logOutput(d.args[0])
	if !ok {
		c.skip(d, "fit writes its access log to stdout, stderr or a file")
		return
	}
	l := &outAccessLog{Output: output}
	for i, a := range d.args[1:] {
		key, value, _ := strings.Cut(a, "=")
		switch {
		case i == 0 && !strings.Contains(a, "="):
			c.warn(d.line, "access_log: the format %s is not converted: fit writes its lines as JSON, text or line, with fields of its own", a)
		case key == "buffer":
			if n, ok := size(value); ok && n > 0 {
				l.BufferBytes = n
			} else {
				c.warn(d.line, "access_log: %s: not a size", a)
			}
		case key == "flush":
			if t, ok := duration([]string{value}); ok {
				l.FlushInterval = t
			} else {
				c.warn(d.line, "access_log: %s: not a time", a)
			}
		case key == "if":
			c.warn(d.line, "access_log: %s is not converted: fit writes a line for every request, or as its level says", a)
		default:
			c.warn(d.line, "access_log: %s is not converted", a)
		}
	}
	if prev := c.out.AccessLog; prev != nil {
		if *prev != *l {
			c.warn(d.line, "access_log: skipped: fit has one access log, which line %d sets", c.accessLogLine)
		}
		return
	}
	if where == "server" || where == "location" {
		c.warn(d.line, "access_log: fit has one access log, for every request, not one for each %s", where)
	}
	c.out.AccessLog = l
	c.accessLogLine = d.line
}
