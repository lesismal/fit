package config

import (
	"maps"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/router"
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// Validate checks what decoding alone cannot: names that must be unique,
// references between parts of the config, and the form of addresses, hosts
// and paths. Whether a module exists, and whether its params are right, is
// for package runtime to check when it compiles the config.
func (c *Config) Validate() Errors {
	var errs Errors
	add := func(v Value, format string, args ...any) {
		errs = append(errs, v.Errorf(format, args...))
	}

	auto := c.AutoHTTPS
	switch {
	case len(c.Listeners) > 0:
	case !auto.on():
		errs = append(errs, &Error{Path: "listeners", Msg: "at least one listener is needed"})
	case len(c.autoNames("")) == 0:
		errs = append(errs, &Error{Path: "listeners", Msg: "at least one listener is needed, or a route that matches a host name for HTTPS by default"})
	case auto.redirects() && auto.HTTP() == auto.HTTPS():
		add(auto.At.Field("http_port"), "the HTTP port is the HTTPS port, %d", auto.HTTP())
	}
	listeners := map[string]bool{}
	if len(c.Listeners) == 0 {
		// The ones HTTPS by default makes, which routes may name.
		effective, _ := c.Effective()
		for _, l := range effective {
			listeners[l.Name] = true
		}
	}
	addresses := map[string]string{}
	for _, l := range c.Listeners {
		switch {
		case !namePattern.MatchString(l.Name):
			add(l.At.Field("name"), "%q is not a valid name: use letters, digits, '_', '.' and '-', starting with a letter or digit", l.Name)
		case listeners[l.Name]:
			add(l.At.Field("name"), "another listener is already named %q", l.Name)
		}
		listeners[l.Name] = true

		if port, msg := checkAddress(l.Address); msg != "" {
			add(l.At.Field("address"), "%s", msg)
		} else if other, ok := addresses[l.Address]; ok {
			add(l.At.Field("address"), "listener %q already listens on %s", other, l.Address)
		} else if port != 0 {
			// Port 0 picks a free port, so two of them do not clash.
			addresses[l.Address] = l.Name
		}

		seen := map[string]bool{}
		for i, p := range l.Protocols {
			switch {
			case seen[p]:
				add(l.At.Field("protocols").Index(i), "%q is listed twice", p)
			case p == ProtocolH2 && l.TLS == nil:
				add(l.At.Field("protocols").Index(i), "h2 needs tls; h2c is HTTP/2 in the clear")
			case p == ProtocolH3 && l.TLS == nil:
				add(l.At.Field("protocols").Index(i), "h3 needs tls")
			case p == ProtocolH2C && l.TLS != nil:
				add(l.At.Field("protocols").Index(i), "h2c is HTTP/2 in the clear, which a listener with tls does not serve; use h2")
			}
			seen[p] = true
		}
		if t := l.TLS; t != nil {
			switch {
			case !emptyTLS(t):
			case !auto.on():
				add(t.At, "a certificate, a name for acme or on_demand is needed")
			case len(c.autoNames(l.Name)) == 0:
				add(t.At, "a certificate, a name for acme or on_demand is needed, or a route on this listener that matches a host name for HTTPS by default")
			}
			if t.OnDemand && (c.ACME == nil || c.ACME.OnDemand == nil) {
				add(t.At.Field("on_demand"), "on_demand needs acme.on_demand, which says whom to ask whether a name may have a certificate")
			}
			names := map[string]bool{}
			for i, name := range t.ACME {
				switch msg := checkHost(name); {
				case msg != "":
					add(t.At.Field("acme").Index(i), "%s", msg)
				case strings.HasPrefix(name, "*."):
					add(t.At.Field("acme").Index(i), "%q is a wildcard, which the HTTP-01 challenge cannot obtain", name)
				case net.ParseIP(strings.Trim(name, "[]")) != nil:
					add(t.At.Field("acme").Index(i), "%q is an IP address; acme takes host names", name)
				case names[strings.ToLower(name)]:
					add(t.At.Field("acme").Index(i), "%q is listed twice", name)
				}
				names[strings.ToLower(name)] = true
			}
		}
	}

	routes := map[string]bool{}
	for _, r := range c.Routes {
		if r.Name != "" {
			switch {
			case !namePattern.MatchString(r.Name):
				add(r.At.Field("name"), "%q is not a valid name: use letters, digits, '_', '.' and '-', starting with a letter or digit", r.Name)
			case routes[r.Name]:
				add(r.At.Field("name"), "another route is already named %q", r.Name)
			}
			routes[r.Name] = true
		}
		for i, name := range r.Listeners {
			if !listeners[name] {
				add(r.At.Field("listeners").Index(i), "there is no listener named %q", name)
			}
		}
		if r.Match != nil {
			for i, h := range r.Match.Host {
				if msg := checkHost(h); msg != "" {
					add(r.Match.At.Field("host").Index(i), "%s", msg)
				}
			}
			for i, p := range r.Match.Path {
				if !strings.HasPrefix(p, "/") {
					add(r.Match.At.Field("path").Index(i), "%q does not start with '/'", p)
				} else if strings.Contains(strings.TrimSuffix(p, "*"), "*") {
					add(r.Match.At.Field("path").Index(i), "%q has a '*' other than at its end", p)
				}
			}
			for i, e := range r.Match.PathRegexp {
				if _, err := router.PathRegexp(e); err != nil {
					add(r.Match.At.Field("path_regexp").Index(i), "%v", err)
				}
			}
			for i, m := range r.Match.Method {
				if !isToken(m) {
					add(r.Match.At.Field("method").Index(i), "%q is not a valid method", m)
				}
			}
			for _, name := range slices.Sorted(maps.Keys(r.Match.Header)) {
				if !isToken(name) {
					add(r.Match.At.Field("header").Field(name), "%q is not a valid header name", name)
				}
			}
			for _, key := range slices.Sorted(maps.Keys(r.Match.Query)) {
				if key == "" {
					add(r.Match.At.Field("query").Field(key), "the query parameter's name is empty")
				}
			}
			for i, s := range r.Match.RemoteIP {
				if _, err := router.ParsePrefix(s); err != nil {
					add(r.Match.At.Field("remote_ip").Index(i), "%v", err)
				}
			}
			if r.Match.Expr != "" {
				if _, err := router.ParseExpr(r.Match.Expr); err != nil {
					add(r.Match.At.Field("expr"), "%v", err)
				}
			}
		}
		for _, m := range r.Middleware {
			if m.Module == "" {
				add(m.At.Field("module"), "the module name is empty")
			}
		}
		if r.Handler.Module == "" {
			add(r.Handler.At.Field("module"), "the module name is empty")
		}
	}

	if a := c.Admin; a != nil {
		if network, address := a.Network(); network == "unix" {
			if address == "" {
				add(a.At.Field("address"), "%q has no path after %q", a.Address, AdminUnixPrefix)
			}
		} else if _, msg := checkAddress(address); msg != "" {
			add(a.At.Field("address"), "%s", msg)
		} else if host, _, _ := net.SplitHostPort(address); !isLoopback(host) && a.TokenFile == "" {
			add(a.At.Field("address"), "%q is not a loopback address: the Admin API can change everything fit serves, so listen on localhost, 127.0.0.1, [::1] or a Unix socket, or set token_file", address)
		}
	}

	if a := c.ACME; a != nil {
		if a.Directory != "" {
			if u, err := url.Parse(a.Directory); err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" {
				add(a.At.Field("directory"), "%q is not an http or https URL", a.Directory)
			}
		}
		if o := a.OnDemand; o != nil {
			if u, err := url.Parse(o.Ask); err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" {
				add(o.At.Field("ask"), "%q is not an http or https URL", o.Ask)
			}
		}
		for i, ch := range a.Challenges {
			if slices.Index(a.Challenges, ch) < i {
				add(a.At.Field("challenges").Index(i), "%q is listed twice", ch)
			}
		}
	}
	if m := c.Metrics; m != nil && c.Admin == nil {
		add(m.At, "the Admin API serves metrics: set admin too")
	}

	upstreams := map[string]bool{}
	for _, u := range c.Upstreams {
		switch {
		case !namePattern.MatchString(u.Name):
			add(u.At.Field("name"), "%q is not a valid name: use letters, digits, '_', '.' and '-', starting with a letter or digit", u.Name)
		case upstreams[u.Name]:
			add(u.At.Field("name"), "another upstream is already named %q", u.Name)
		}
		upstreams[u.Name] = true
		if len(u.Addresses) == 0 {
			add(u.At.Field("addresses"), "an upstream needs at least one address")
		}
		if len(u.Weights) > 0 && len(u.Weights) != len(u.Addresses) {
			add(u.At.Field("weights"), "%d weights for %d addresses; give one weight for each address", len(u.Weights), len(u.Addresses))
		}
		if len(u.Weights) > 0 && u.LoadBalancing == "round_robin" {
			add(u.At.Field("load_balancing"), "round_robin takes no weights; use weighted_round_robin or least_conn")
		}
		switch keyed := u.LoadBalancing == "header_hash" || u.LoadBalancing == "cookie_hash"; {
		case keyed && u.HashKey == "":
			add(u.At.Field("hash_key"), "%s needs hash_key, the name of the %s to hash", u.LoadBalancing, strings.TrimSuffix(u.LoadBalancing, "_hash"))
		case !keyed && u.HashKey != "":
			add(u.At.Field("hash_key"), "hash_key is for header_hash and cookie_hash")
		case keyed && !isToken(u.HashKey):
			add(u.At.Field("hash_key"), "%q is not a valid %s name", u.HashKey, strings.TrimSuffix(u.LoadBalancing, "_hash"))
		}
		if s := u.StickyCookie; s != nil {
			if s.Name != "" && !isToken(s.Name) {
				add(s.At.Field("name"), "%q is not a valid cookie name", s.Name)
			}
			if s.Path != "" && (!strings.HasPrefix(s.Path, "/") || strings.ContainsAny(s.Path, ";\x00\r\n")) {
				add(s.At.Field("path"), "%q is not a cookie path: it must start with '/' and hold no ';'", s.Path)
			}
		}
		if u.Protocol != "h2" {
			if u.TLSServerName != "" {
				add(u.At.Field("tls_server_name"), "tls_server_name needs protocol h2")
			}
			if u.TLSCAFile != "" {
				add(u.At.Field("tls_ca_file"), "tls_ca_file needs protocol h2")
			}
		}
		if h := u.HealthCheck; h != nil {
			if h.Type == HealthTCP {
				for _, f := range []struct {
					name string
					set  bool
				}{{"path", h.Path != ""}, {"host", h.Host != ""}, {"expect_status", len(h.ExpectStatus) > 0}} {
					if f.set {
						add(h.At.Field(f.name), "%s is for http health checks; a tcp one only opens a connection", f.name)
					}
				}
			}
			if h.Path != "" && !strings.HasPrefix(h.Path, "/") {
				add(h.At.Field("path"), "%q does not start with '/'", h.Path)
			}
			if h.Host != "" {
				if msg := checkHost(h.Host); msg != "" || strings.HasPrefix(h.Host, "*.") {
					add(h.At.Field("host"), "%q is not a host name or IP address", h.Host)
				}
			}
			if h.Wait() > h.Every() {
				add(h.At.Field("timeout"), "the timeout, %s, is longer than the interval, %s", h.Wait(), h.Every())
			}
			if h.Spread() >= h.Every() {
				add(h.At.Field("jitter"), "the jitter, %s, is not less than the interval, %s", h.Spread(), h.Every())
			}
		}
		seen := map[string]bool{}
		for i, addr := range u.Addresses {
			at := u.At.Field("addresses").Index(i)
			if msg := checkUpstreamAddress(addr); msg != "" {
				add(at, "%s", msg)
			} else if seen[addr] {
				add(at, "%q is listed twice", addr)
			}
			seen[addr] = true
		}
	}
	return errs
}

// isToken reports whether s is an HTTP token (RFC 9110), as methods,
// header field and cookie names are.
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0 {
			continue
		}
		return false
	}
	return true
}

// isLoopback reports whether host is localhost or a loopback IP address.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkUpstreamAddress says what is wrong with addr as an upstream's
// address, or returns "".
func checkUpstreamAddress(addr string) string {
	if strings.Contains(addr, "://") {
		return addr + " is a URL; write the host and port alone, as in \"10.0.0.1:8080\""
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return strconv.Quote(addr) + " is not a host and port, as in \"10.0.0.1:8080\""
	}
	if host == "" {
		return strconv.Quote(addr) + " has no host"
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return strconv.Quote(addr) + " has no valid port"
	}
	return ""
}

// checkAddress returns address's port, or why it is not a valid address.
func checkAddress(address string) (uint64, string) {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return 0, "\"" + address + "\" is not a host and port, as in \":8080\" or \"127.0.0.1:8080\""
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return 0, "\"" + port + "\" is not a port between 0 and 65535"
	}
	return n, ""
}

var hostLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$`)

func checkHost(host string) string {
	name := strings.TrimPrefix(host, "*.")
	if name == "" {
		return "the host is empty"
	}
	if net.ParseIP(strings.Trim(name, "[]")) != nil {
		if strings.HasPrefix(host, "*.") {
			return "\"" + host + "\" puts a wildcard on an IP address"
		}
		return ""
	}
	for label := range strings.SplitSeq(name, ".") {
		if !hostLabel.MatchString(label) {
			return "\"" + host + "\" is not a valid host name"
		}
	}
	return ""
}
