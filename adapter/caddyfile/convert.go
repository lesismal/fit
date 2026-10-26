// Package caddyfile converts a common subset of Caddy's Caddyfile to fit's
// native JSON: the global options fit has a place for, snippets and their
// imports, sites by their addresses, named matchers, handle, handle_path
// and route blocks, and in them reverse_proxy, file_server, respond,
// php_fastcgi, root, try_files, redir, rewrite, uri, header,
// request_header, encode, basic_auth, forward_auth, request_body, tls and
// log. Every directive it does not convert is reported, with its line, as
// a warning; fit adapt lists them.
package caddyfile

import (
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/adapter"
	"github.com/lesismal/fit/config"
)

func init() {
	adapter.Register("caddyfile", Convert)
}

// The native config the converter writes, its fields in the order the
// native config has them.
type (
	outConfig struct {
		Listeners []*outListener `json:"listeners,omitempty"`
		Routes    []*outRoute    `json:"routes"`
		Upstreams []*outUpstream `json:"upstreams,omitempty"`
		Log       *outLog        `json:"log,omitempty"`
		AccessLog *outAccessLog  `json:"access_log,omitempty"`
		Admin     *outAdmin      `json:"admin,omitempty"`
		AutoHTTPS *outAutoHTTPS  `json:"auto_https,omitempty"`
		Metrics   *struct{}      `json:"metrics,omitempty"`
		ACME      *outACME       `json:"acme,omitempty"`
	}
	outListener struct {
		Name      string   `json:"name"`
		Address   string   `json:"address"`
		Protocols []string `json:"protocols,omitempty"`
		TLS       *outTLS  `json:"tls,omitempty"`
		line      int
	}
	outTLS struct {
		Certificates []outCert      `json:"certificates,omitempty"`
		ACME         []string       `json:"acme,omitempty"`
		OnDemand     bool           `json:"on_demand,omitempty"`
		ClientAuth   *outClientAuth `json:"client_auth,omitempty"`
	}
	outCert struct {
		CertFile string `json:"cert_file"`
		KeyFile  string `json:"key_file"`
	}
	outClientAuth struct {
		CAFiles []string `json:"ca_files"`
		Mode    string   `json:"mode,omitempty"`
	}
	outUpstream struct {
		Name                  string     `json:"name"`
		Addresses             []string   `json:"addresses"`
		Weights               []int      `json:"weights,omitempty"`
		LoadBalancing         string     `json:"load_balancing,omitempty"`
		HashKey               string     `json:"hash_key,omitempty"`
		StickyCookie          *outSticky `json:"sticky_cookie,omitempty"`
		MaxConns              int        `json:"max_conns,omitempty"`
		MaxIdleConns          *int       `json:"max_idle_conns,omitempty"`
		IdleTimeout           string     `json:"idle_timeout,omitempty"`
		ConnectTimeout        string     `json:"connect_timeout,omitempty"`
		Protocol              string     `json:"protocol,omitempty"`
		TLSServerName         string     `json:"tls_server_name,omitempty"`
		TLSCAFile             string     `json:"tls_ca_file,omitempty"`
		TLSCertFile           string     `json:"tls_cert_file,omitempty"`
		TLSKeyFile            string     `json:"tls_key_file,omitempty"`
		MaxFails              *int       `json:"max_fails,omitempty"`
		FailTimeout           string     `json:"fail_timeout,omitempty"`
		ResponseHeaderTimeout string     `json:"response_header_timeout,omitempty"`
		ResponseIdleTimeout   string     `json:"response_idle_timeout,omitempty"`
		HealthCheck           *outHealth `json:"health_check,omitempty"`
		line                  int
	}
	outSticky struct {
		Name string `json:"name,omitempty"`
	}
	outHealth struct {
		Path         string `json:"path,omitempty"`
		Host         string `json:"host,omitempty"`
		Port         int    `json:"port,omitempty"`
		ExpectStatus []int  `json:"expect_status,omitempty"`
		Interval     string `json:"interval,omitempty"`
		Timeout      string `json:"timeout,omitempty"`
		Fails        int    `json:"fails,omitempty"`
		Passes       int    `json:"passes,omitempty"`
	}
	outRoute struct {
		Name       string    `json:"name,omitempty"`
		Listeners  []string  `json:"listeners,omitempty"`
		Match      *outMatch `json:"match,omitempty"`
		Middleware []outRef  `json:"middleware,omitempty"`
		Handler    outRef    `json:"handler"`
		line       int
		mwLines    []int
		hLine      int
	}
	outRef struct {
		Module string         `json:"module"`
		Params map[string]any `json:"params,omitempty"`
	}
	outLog struct {
		Level  string `json:"level,omitempty"`
		Output string `json:"output,omitempty"`
		Format string `json:"format,omitempty"`
	}
	outAccessLog struct {
		Output string `json:"output,omitempty"`
		Level  string `json:"level,omitempty"`
	}
	outAdmin struct {
		Address string `json:"address,omitempty"`
	}
	outAutoHTTPS struct {
		Disable          bool `json:"disable,omitempty"`
		DisableRedirects bool `json:"disable_redirects,omitempty"`
		HTTPPort         int  `json:"http_port,omitempty"`
		HTTPSPort        int  `json:"https_port,omitempty"`
	}
	outACME struct {
		Directory string       `json:"directory,omitempty"`
		Email     string       `json:"email,omitempty"`
		CAFile    string       `json:"ca_file,omitempty"`
		Storage   string       `json:"storage,omitempty"`
		OnDemand  *outOnDemand `json:"on_demand,omitempty"`
	}
	outOnDemand struct {
		Ask      string `json:"ask"`
		Rate     int    `json:"rate,omitempty"`
		Interval string `json:"interval,omitempty"`
	}
)

// converter holds what the conversion has made so far.
type converter struct {
	out       outConfig
	warnings  []string
	names     map[string]bool
	upstreams map[string]*outUpstream
	listeners map[string]*outListener
	httpPort  int
	httpsPort int
	autoOff   bool
	noRedir   bool
	noCerts   bool
	protocols []string
	adminOff  bool
	logLine   int
	// globalLogLine is the line of the global log fit's log is made from.
	globalLogLine int
	scopes        int
}

// Convert converts a Caddyfile to fit's native JSON.
func Convert(data []byte) (*adapter.Result, error) {
	blocks, warnings, err := parse(data)
	if err != nil {
		return nil, err
	}
	c := &converter{warnings: warnings, names: map[string]bool{}, upstreams: map[string]*outUpstream{}, listeners: map[string]*outListener{},
		httpPort: config.DefaultHTTPPort, httpsPort: config.DefaultHTTPSPort}
	var sites []*site
	for i, b := range blocks {
		if len(b.keys) == 0 {
			if i == 0 {
				c.global(b.dirs)
			}
			continue
		}
		if s := c.site(b); s != nil {
			sites = append(sites, s)
		}
	}
	if len(sites) == 0 {
		return nil, fmt.Errorf("the Caddyfile has no site to convert")
	}
	c.serve(sites)
	if len(c.out.Routes) == 0 {
		return nil, fmt.Errorf("none of the Caddyfile's sites converted")
	}
	if !c.adminOff && c.out.Admin == nil {
		// Caddy serves its admin endpoint unless told not to; fit's Admin
		// API takes its place.
		c.out.Admin = &outAdmin{}
	}
	return c.result()
}

func (c *converter) result() (*adapter.Result, error) {
	// Warnings once each, in the order of the Caddyfile's lines.
	var ws []string
	for _, w := range c.warnings {
		if !slices.Contains(ws, w) {
			ws = append(ws, w)
		}
	}
	slices.SortStableFunc(ws, func(a, b string) int { return warningLine(a) - warningLine(b) })
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
		pos[fmt.Sprintf("routes[%d].handler", i)] = r.hLine
		for j, l := range r.mwLines {
			pos[fmt.Sprintf("routes[%d].middleware[%d]", i, j)] = l
		}
	}
	if c.out.AccessLog != nil {
		pos["access_log"] = c.logLine
	}
	if c.out.Log != nil {
		pos["log"] = c.globalLogLine
	}
	return &adapter.Result{JSON: out, Warnings: ws, Position: func(path string) (int, int, bool) {
		line, ok := pos[path]
		return line, 1, ok && line > 0
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

func (c *converter) acme() *outACME {
	if c.out.ACME == nil {
		c.out.ACME = &outACME{}
	}
	return c.out.ACME
}

func (c *converter) autoHTTPS() *outAutoHTTPS {
	if c.out.AutoHTTPS == nil {
		c.out.AutoHTTPS = &outAutoHTTPS{}
	}
	return c.out.AutoHTTPS
}

// global converts the global options block.
func (c *converter) global(dirs []*directive) {
	for _, d := range dirs {
		one := len(d.args) == 1
		switch d.name {
		case "email":
			if one {
				c.acme().Email = d.args[0]
				continue
			}
		case "acme_ca":
			if one {
				c.acme().Directory = d.args[0]
				continue
			}
		case "acme_ca_root":
			if one {
				c.acme().CAFile = d.args[0]
				continue
			}
		case "storage":
			if len(d.args) >= 1 && d.args[0] == "file_system" {
				path := ""
				if len(d.args) == 2 {
					path = d.args[1]
				}
				for _, e := range d.block {
					if e.name == "root" && len(e.args) == 1 {
						path = e.args[0]
					}
				}
				if path != "" {
					c.acme().Storage = path
					continue
				}
			}
			c.skip(d, "fit keeps certificates in a directory, or in Redis, etcd or Consul through acme.shared_storage, written by hand")
			continue
		case "admin":
			switch {
			case one && d.args[0] == "off":
				c.adminOff = true
				continue
			case one:
				c.out.Admin = &outAdmin{Address: strings.TrimPrefix(d.args[0], "tcp/")}
				if len(d.block) > 0 {
					c.warn(d.line, "admin: its block is not converted")
				}
				continue
			case len(d.args) == 0:
				c.out.Admin = &outAdmin{}
				continue
			}
		case "auto_https":
			if one {
				switch d.args[0] {
				case "off":
					c.autoOff = true
					c.autoHTTPS().Disable = true
					continue
				case "disable_redirects":
					c.noRedir = true
					c.autoHTTPS().DisableRedirects = true
					continue
				case "disable_certs":
					c.noCerts = true
					continue
				case "ignore_loaded_certs", "prefer_wildcard":
					continue
				}
			}
		case "http_port", "https_port":
			if one {
				if n, err := strconv.Atoi(d.args[0]); err == nil && n > 0 && n < 65536 {
					if d.name == "http_port" {
						c.httpPort, c.autoHTTPS().HTTPPort = n, n
					} else {
						c.httpsPort, c.autoHTTPS().HTTPSPort = n, n
					}
					continue
				}
			}
		case "on_demand_tls":
			c.onDemandTLS(d)
			continue
		case "servers":
			c.servers(d)
			continue
		case "debug":
			if len(d.args) == 0 {
				c.globalLog(d.line).Level = "debug"
				continue
			}
		case "log":
			c.globalLogDirective(d)
			continue
		case "grace_period", "shutdown_delay", "order", "persist_config", "skip_install_trust", "local_certs",
			"key_type", "renew_interval", "ocsp_interval", "ocsp_stapling", "preferred_chains", "cert_issuer", "pki",
			"events", "filesystem", "default_sni", "fallback_sni", "default_bind", "cert_lifetime", "metrics":
			c.skip(d, "not converted: fit's defaults stand in for it")
			continue
		}
		c.skip(d, "not converted")
	}
}

func (c *converter) onDemandTLS(d *directive) {
	od := &outOnDemand{}
	for _, e := range d.block {
		switch {
		case e.name == "ask" && len(e.args) == 1:
			od.Ask = e.args[0]
		case e.name == "permission" && len(e.args) == 1 && e.args[0] == "http":
			for _, f := range e.block {
				if f.name == "endpoint" && len(f.args) == 1 {
					od.Ask = f.args[0]
				}
			}
		case e.name == "interval" && len(e.args) == 1:
			if t, ok := duration(e.args[0]); ok {
				od.Interval = t
			}
		case e.name == "burst" && len(e.args) == 1:
			if n, err := strconv.Atoi(e.args[0]); err == nil && n > 0 {
				od.Rate = n
			}
		default:
			c.skip(e, "not converted")
		}
	}
	if od.Ask == "" {
		c.skip(d, "on-demand TLS needs an ask URL")
		return
	}
	c.acme().OnDemand = od
}

func (c *converter) servers(d *directive) {
	for _, e := range d.block {
		switch e.name {
		case "protocols":
			c.protocols = nil
			for _, p := range e.args {
				switch p {
				case "h1":
					c.protocols = append(c.protocols, config.ProtocolHTTP1)
				case "h2", "h2c", "h3":
					c.protocols = append(c.protocols, p)
				default:
					c.warn(e.line, "protocols %s: skipped: not converted", p)
				}
			}
		default:
			c.skip(e, "not converted: fit's limits and timeouts are set at the top of its config")
		}
	}
	if len(d.args) > 0 {
		c.warn(d.line, "servers %s: its options apply to every listener", strings.Join(d.args, " "))
	}
}

// site is a site block: its addresses, and what its directives say of how
// it is served.
type site struct {
	block    *serverBlock
	addrs    []*siteAddr
	name     string
	tls      siteTLS
	bind     []string
	matchers map[string]*outMatch
	badMatch map[string]string
}

type siteAddr struct {
	raw, host, port, path string
	scheme                string
	tls                   bool
}

// siteTLS is what a site's tls directive says.
type siteTLS struct {
	certs      []outCert
	onDemand   bool
	internal   bool
	clientAuth *outClientAuth
	line       int
}

func (c *converter) site(b *serverBlock) *site {
	s := &site{block: b, matchers: map[string]*outMatch{}, badMatch: map[string]string{}}
	for _, d := range b.dirs {
		switch d.name {
		case "tls":
			c.tlsDirective(s, d)
		case "bind":
			s.bind = append(s.bind, d.args...)
		case "log":
			c.log(d)
		}
	}
	c.collectMatchers(s, b.dirs)
	for _, k := range b.keys {
		a, ok := c.address(b.line, k, s)
		if ok {
			s.addrs = append(s.addrs, a)
		}
	}
	if len(s.addrs) == 0 {
		c.warn(b.line, "site %s: skipped: none of its addresses converted", strings.Join(b.keys, ", "))
		return nil
	}
	s.name = s.addrs[0].host
	if s.name == "" {
		s.name = "port" + s.addrs[0].port
	}
	return s
}

// collectMatchers converts the named matchers a site defines, in its block
// and the blocks in it.
func (c *converter) collectMatchers(s *site, dirs []*directive) {
	for _, d := range dirs {
		if strings.HasPrefix(d.name, "@") {
			m, why := namedMatcher(d)
			if why != "" {
				s.badMatch[d.name] = why
				c.warn(d.line, "%s: %s; what uses it is skipped", d.name, why)
				continue
			}
			s.matchers[d.name] = m
			continue
		}
		switch d.name {
		case "handle", "handle_path", "route":
			c.collectMatchers(s, d.block)
		}
	}
}

// address reads a site address: [scheme://][host][:port][/path].
func (c *converter) address(line int, raw string, s *site) (*siteAddr, bool) {
	a := &siteAddr{raw: raw}
	rest := raw
	if scheme, r, ok := strings.Cut(rest, "://"); ok {
		if scheme != "http" && scheme != "https" {
			c.warn(line, "site address %s: skipped: fit serves http:// and https://", raw)
			return nil, false
		}
		a.scheme, rest = scheme, r
	}
	if strings.HasPrefix(rest, "unix/") || strings.Contains(rest, "{") {
		c.warn(line, "site address %s: skipped: not converted", raw)
		return nil, false
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest, a.path = rest[:i], rest[i:]
		if !simplePath(a.path) {
			c.warn(line, "site address %s: skipped: fit takes a path that is exact or ends in *", raw)
			return nil, false
		}
	}
	if h, p, err := net.SplitHostPort(rest); err == nil {
		a.host, a.port = h, p
	} else {
		a.host = strings.Trim(rest, "[]")
	}
	a.host = strings.ToLower(a.host)
	if a.port != "" {
		if n, err := strconv.Atoi(a.port); err != nil || n < 0 || n > 65535 {
			c.warn(line, "site address %s: skipped: not a port", raw)
			return nil, false
		}
	}
	if a.host == "*" {
		a.host = ""
	}
	if strings.Count(a.host, "*") > 1 || strings.Contains(a.host, "*") && !strings.HasPrefix(a.host, "*.") {
		c.warn(line, "site address %s: skipped: fit takes names and a leading \"*.\"", raw)
		return nil, false
	}
	hasCerts := len(s.tls.certs) > 0 || s.tls.onDemand
	public := a.host != "" && (config.AutoHTTPSName(a.host) || strings.HasPrefix(a.host, "*.") && hasCerts)
	switch {
	case a.scheme == "http":
		a.tls = false
	case a.scheme == "https":
		a.tls = true
	case a.port == strconv.Itoa(c.httpPort):
		a.tls = false
	case public || hasCerts:
		a.tls = true
	default:
		a.tls = false
		if a.host != "" {
			c.warn(line, "site address %s: served over HTTP: Caddy serves it over HTTPS with a certificate of its internal CA, which fit has not", raw)
		}
	}
	if a.port == "" {
		a.port = strconv.Itoa(c.httpPort)
		if a.tls {
			a.port = strconv.Itoa(c.httpsPort)
		}
	}
	return a, true
}

func (c *converter) tlsDirective(s *site, d *directive) {
	t := &s.tls
	t.line = d.line
	switch len(d.args) {
	case 1:
		switch {
		case d.args[0] == "internal":
			t.internal = true
			c.warn(d.line, "tls internal: fit has no internal CA: write certificate files, or let ACME obtain certificates for public names")
		case strings.Contains(d.args[0], "@"):
			c.email(d.line, d.args[0])
		default:
			c.skip(d, "tls takes a certificate file and a key file, an email address or internal")
		}
	case 2:
		t.certs = append(t.certs, outCert{d.args[0], d.args[1]})
	}
	for _, e := range d.block {
		switch e.name {
		case "on_demand":
			t.onDemand = true
		case "client_auth":
			ca := &outClientAuth{}
			for _, f := range e.block {
				switch {
				case f.name == "mode" && len(f.args) == 1:
					switch f.args[0] {
					case "require_and_verify":
						ca.Mode = "require"
					case "verify_if_given":
						ca.Mode = "verify_if_given"
					default:
						c.warn(f.line, "client_auth mode %s: fit requires and verifies certificates, or verifies them if given", f.args[0])
						ca.Mode = "verify_if_given"
					}
				case f.name == "trusted_ca_cert_file" && len(f.args) == 1:
					ca.CAFiles = append(ca.CAFiles, f.args[0])
				case f.name == "trust_pool" && len(f.args) >= 2 && f.args[0] == "file":
					ca.CAFiles = append(ca.CAFiles, f.args[1:]...)
				case f.name == "trust_pool" && len(f.args) == 1 && f.args[0] == "file":
					for _, g := range f.block {
						if g.name == "pem_file" {
							ca.CAFiles = append(ca.CAFiles, g.args...)
						}
					}
				default:
					c.skip(f, "not converted")
				}
			}
			if len(ca.CAFiles) == 0 {
				c.skip(e, "client_auth needs CA files")
				continue
			}
			if ca.Mode == "" {
				// Caddy requires and verifies certificates once CAs are set.
				ca.Mode = "require"
			}
			t.clientAuth = ca
		case "protocols", "ciphers", "curves", "alpn", "key_type":
			c.skip(e, "fit picks TLS versions, ciphers and curves itself")
		case "issuer":
			if len(e.args) > 0 && e.args[0] == "internal" {
				t.internal = true
				c.warn(e.line, "issuer internal: fit has no internal CA")
				continue
			}
			for _, f := range e.block {
				switch {
				case f.name == "email" && len(f.args) == 1:
					c.email(f.line, f.args[0])
				case f.name == "dir" && len(f.args) == 1:
					c.acme().Directory = f.args[0]
				default:
					c.skip(f, "not converted")
				}
			}
		case "ca":
			if len(e.args) == 1 {
				c.acme().Directory = e.args[0]
				continue
			}
			c.skip(e, "not converted")
		case "dns":
			c.skip(e, "fit answers ACME's HTTP-01 and TLS-ALPN-01 challenges, not DNS-01")
		default:
			c.skip(e, "not converted")
		}
	}
}

func (c *converter) email(line int, e string) {
	a := c.acme()
	if a.Email != "" && a.Email != e {
		c.warn(line, "tls %s: fit has one ACME email, %s", e, a.Email)
		return
	}
	a.Email = e
}

// log converts a site's log to the access log, which fit has one of.
func (c *converter) log(d *directive) {
	output, level := "stdout", ""
	for _, e := range d.block {
		switch e.name {
		case "output":
			switch {
			case len(e.args) == 1 && (e.args[0] == "stdout" || e.args[0] == "stderr"):
				output = e.args[0]
			case len(e.args) == 2 && e.args[0] == "file":
				output = e.args[1]
				if len(e.block) > 0 {
					c.warn(e.line, "output file: rolling is not converted: fit appends to the file, for logrotate to rotate")
				}
			case len(e.args) == 1 && e.args[0] == "discard":
				return
			default:
				c.skip(e, "fit writes to stdout, stderr or a file")
			}
		case "format":
			if len(e.args) == 0 || e.args[0] != "json" {
				c.skip(e, "fit writes access log lines as JSON")
			}
		case "level":
			if lv, ok := c.logLevel(e); ok {
				level = accessLevel(lv)
			}
		default:
			c.skip(e, "not converted")
		}
	}
	if l := c.out.AccessLog; l != nil && (l.Output != output || l.Level != level) {
		c.warn(d.line, "log: fit has one access log, written to %s", l.Output)
		return
	}
	c.out.AccessLog = &outAccessLog{Output: output, Level: level}
	c.logLine = d.line
}

// logLevel reads a level directive's Caddy level as fit's: DEBUG, INFO,
// WARN and ERROR, and PANIC and FATAL as error, fit's highest.
func (c *converter) logLevel(e *directive) (string, bool) {
	if len(e.args) != 1 {
		c.skip(e, "not converted")
		return "", false
	}
	switch strings.ToUpper(e.args[0]) {
	case "DEBUG":
		return "debug", true
	case "INFO":
		return "info", true
	case "WARN", "WARNING":
		return "warn", true
	case "ERROR":
		return "error", true
	case "PANIC", "FATAL", "DPANIC":
		c.warn(e.line, "level %s is fit's error, its highest", e.args[0])
		return "error", true
	}
	c.skip(e, "not a level")
	return "", false
}

// accessLevel is the access log's level for a log's: info writes every
// request, as debug does.
func accessLevel(level string) string {
	if level == "debug" || level == "info" {
		return ""
	}
	return level
}

// globalLog returns fit's log, made from the global options, which line
// sets first.
func (c *converter) globalLog(line int) *outLog {
	if c.out.Log == nil {
		// Caddy writes its log to stderr unless told otherwise.
		c.out.Log = &outLog{Output: "stderr"}
		c.globalLogLine = line
	}
	return c.out.Log
}

// globalLogDirective converts the global log option: the default logger, or
// one named, is fit's log, unless it includes only the access logs
// (http.log.access), when it is fit's access log.
func (c *converter) globalLogDirective(d *directive) {
	if len(d.args) > 1 {
		c.skip(d, "not converted")
		return
	}
	access := false
	for _, e := range d.block {
		if e.name == "include" {
			access = len(e.args) > 0
			for _, a := range e.args {
				access = access && strings.HasPrefix(a, "http.log.access")
			}
		}
	}
	if access {
		site := *d
		site.block = slices.DeleteFunc(slices.Clone(d.block), func(e *directive) bool { return e.name == "include" })
		c.log(&site)
		return
	}
	if len(d.args) == 1 && d.args[0] != "default" {
		c.skip(d, "fit has one log: the default logger's is converted")
		return
	}
	var out outLog
	if c.out.Log != nil {
		out = *c.out.Log
	} else {
		out.Output = "stderr"
	}
	for _, e := range d.block {
		switch e.name {
		case "output":
			switch {
			case len(e.args) == 1 && (e.args[0] == "stdout" || e.args[0] == "stderr"):
				out.Output = e.args[0]
			case len(e.args) == 2 && e.args[0] == "file":
				out.Output = e.args[1]
				if len(e.block) > 0 {
					c.warn(e.line, "output file: rolling is not converted: fit appends to the file, for logrotate to rotate")
				}
			default:
				c.skip(e, "fit writes its log to stdout, stderr or a file")
			}
		case "format":
			switch {
			case len(e.args) > 0 && e.args[0] == "json":
				out.Format = "json"
			case len(e.args) > 0 && e.args[0] == "console":
				out.Format = ""
			default:
				c.skip(e, "fit writes its log as text or JSON")
			}
		case "level":
			if lv, ok := c.logLevel(e); ok {
				out.Level = lv
			}
		case "exclude":
			c.skip(e, "fit's log.levels sets the level of each of its loggers, written by hand")
		case "include":
			c.skip(e, "fit's log takes every logger's messages")
		default:
			c.skip(e, "not converted")
		}
	}
	if out.Level == "info" {
		out.Level = ""
	}
	c.globalLog(d.line)
	*c.out.Log = out
}

// group is the addresses of a site served alike: on the same listeners,
// for the same hosts.
type group struct {
	listeners []string
	hosts     []string
	paths     []string
}

// serve makes the listeners the sites need, unless HTTPS by default makes
// them, and the sites' routes.
func (c *converter) serve(sites []*site) {
	auto := !c.autoOff && len(c.protocols) == 0
	for _, s := range sites {
		if len(s.tls.certs) > 0 || s.tls.onDemand || s.tls.clientAuth != nil || len(s.bind) > 0 {
			auto = false
		}
		for _, a := range s.addrs {
			if !a.tls || a.port != strconv.Itoa(c.httpsPort) {
				auto = false
			}
		}
	}
	if auto {
		// fit's HTTPS by default serves the routes on :443, with certificates
		// for their names, and redirects :80 to it, as Caddy does.
		for _, s := range sites {
			g := &group{}
			for _, a := range s.addrs {
				g.hosts = append(g.hosts, a.host)
				g.paths = append(g.paths, a.path)
			}
			c.siteRoutes(s, g)
		}
		return
	}
	redirect := map[string]bool{}
	plain := map[string]bool{}
	for _, s := range sites {
		var groups []*group
		byListener := map[string][]string{}
		var order []string
		paths := map[string][]string{}
		for _, a := range s.addrs {
			binds := s.bind
			if len(binds) == 0 {
				binds = []string{""}
			}
			for _, b := range binds {
				name, ok := c.listener(s, a, b)
				if !ok {
					continue
				}
				if _, seen := byListener[name]; !seen {
					order = append(order, name)
				}
				byListener[name] = append(byListener[name], a.host)
				paths[name] = append(paths[name], a.path)
			}
			if a.tls && a.port == strconv.Itoa(c.httpsPort) && a.host != "" && !s.tls.internal {
				redirect[a.host] = true
			}
			if !a.tls && a.port == strconv.Itoa(c.httpPort) {
				plain[a.host] = true
			}
		}
		for _, name := range order {
			hosts := byListener[name]
			found := false
			for _, g := range groups {
				if sameSet(g.hosts, hosts) && slices.Equal(g.paths, paths[name]) {
					g.listeners = append(g.listeners, name)
					found = true
				}
			}
			if !found {
				groups = append(groups, &group{listeners: []string{name}, hosts: hosts, paths: paths[name]})
			}
		}
		for _, g := range groups {
			c.siteRoutes(s, g)
		}
	}
	c.tlsNames(sites)
	if !c.autoOff && !c.noRedir {
		c.redirects(redirect, plain)
	}
}

// listener returns the name of the listener a site's address is served
// on, made when it is the first there.
func (c *converter) listener(s *site, a *siteAddr, bind string) (string, bool) {
	address := ":" + a.port
	if bind != "" {
		address = net.JoinHostPort(strings.Trim(strings.TrimPrefix(bind, "tcp/"), "[]"), a.port)
	}
	l := c.listeners[address]
	if l == nil {
		scheme := "http"
		if a.tls {
			scheme = "https"
		}
		name := scheme + "_" + strings.NewReplacer(".", "_", ":", "_", "[", "", "]", "").Replace(strings.TrimPrefix(address, ":"))
		l = &outListener{Name: name, Address: address, line: s.block.line}
		if a.tls {
			l.TLS = &outTLS{}
		}
		for _, p := range c.protocols {
			if (p == "h2" || p == "h3") && !a.tls || p == "h2c" && a.tls {
				continue
			}
			l.Protocols = append(l.Protocols, p)
		}
		if len(l.Protocols) == 0 && len(c.protocols) > 0 {
			l.Protocols = []string{config.ProtocolHTTP1}
		}
		c.listeners[address] = l
		c.out.Listeners = append(c.out.Listeners, l)
	} else if (l.TLS != nil) != a.tls {
		c.warn(s.block.line, "site address %s: skipped: another site serves %s with%s TLS", a.raw, address, map[bool]string{true: "out", false: ""}[a.tls])
		return "", false
	}
	if a.tls {
		for _, cert := range s.tls.certs {
			if !slices.Contains(l.TLS.Certificates, cert) {
				l.TLS.Certificates = append(l.TLS.Certificates, cert)
			}
		}
		if s.tls.onDemand {
			if c.out.ACME == nil || c.out.ACME.OnDemand == nil {
				c.warn(s.tls.line, "tls on_demand: skipped: it needs the global on_demand_tls with an ask URL")
			} else {
				l.TLS.OnDemand = true
			}
		}
		if ca := s.tls.clientAuth; ca != nil {
			if l.TLS.ClientAuth != nil && !slices.Equal(l.TLS.ClientAuth.CAFiles, ca.CAFiles) {
				c.warn(s.tls.line, "tls client_auth: %s already verifies clients with other CAs", address)
			} else {
				l.TLS.ClientAuth = ca
			}
		}
	}
	return l.Name, true
}

// tlsNames lists, on a TLS listener that has certificate files or on-demand
// TLS, the names of the sites without files of their own, whose
// certificates ACME obtains. A listener with neither gets them from HTTPS
// by default.
func (c *converter) tlsNames(sites []*site) {
	if c.autoOff || c.noCerts {
		return
	}
	for _, l := range c.out.Listeners {
		if l.TLS == nil || len(l.TLS.Certificates) == 0 && !l.TLS.OnDemand {
			continue
		}
		for _, s := range sites {
			if len(s.tls.certs) > 0 || s.tls.internal {
				continue
			}
			for _, a := range s.addrs {
				if a.tls && (l.Address == ":"+a.port || strings.HasSuffix(l.Address, ":"+a.port) && len(s.bind) > 0) &&
					config.AutoHTTPSName(a.host) && !slices.Contains(l.TLS.ACME, a.host) {
					l.TLS.ACME = append(l.TLS.ACME, a.host)
				}
			}
		}
	}
}

// redirects redirects HTTP to HTTPS for the names served over HTTPS on its
// port, but those a site serves over HTTP itself, as Caddy does.
func (c *converter) redirects(names, plain map[string]bool) {
	var hosts []string
	for h := range names {
		if !plain[h] {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		return
	}
	slices.Sort(hosts)
	address := ":" + strconv.Itoa(c.httpPort)
	l := c.listeners[address]
	if l == nil {
		l = &outListener{Name: "http_" + strconv.Itoa(c.httpPort), Address: address}
		c.listeners[address] = l
		c.out.Listeners = append(c.out.Listeners, l)
	} else if l.TLS != nil {
		c.warn(0, "redirects to HTTPS: skipped: %s serves TLS", address)
		return
	}
	to := "https://{host}{uri}"
	if c.httpsPort != config.DefaultHTTPSPort {
		to = "https://{host}:" + strconv.Itoa(c.httpsPort) + "{uri}"
	}
	c.out.Routes = append(c.out.Routes, &outRoute{
		Name: c.routeName("redirect", "https"), Listeners: []string{l.Name}, Match: &outMatch{Host: hosts},
		Middleware: []outRef{{Module: "redirect", Params: map[string]any{"to": to, "status": 308}}},
		Handler:    outRef{Module: "respond"}, mwLines: []int{0},
	})
}

// routeName returns a route name, unique among the routes, made of the
// site's name and a hint, with what route names may not hold replaced.
func (c *converter) routeName(siteName, hint string) string {
	var b strings.Builder
	for _, r := range siteName + "-" + strings.Trim(hint, "/*") {
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
