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
		} else {
			// A TCP port and a UDP one of the same number do not clash, so
			// a tcp listener and a udp one, or HTTP and HTTP/3, may share
			// an address.
			for _, network := range l.networks() {
				key := network + " " + l.Address
				if other, ok := addresses[key]; ok {
					add(l.At.Field("address"), "listener %q already listens on %s", other, l.Address)
					break
				} else if port != 0 {
					// Port 0 picks a free port, so two of them do not clash.
					addresses[key] = l.Name
				}
			}
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
			case p == ProtocolUDP && len(l.Protocols) > 1:
				add(l.At.Field("protocols").Index(i), "a udp listener serves UDP alone: list udp without other protocols")
			case p == ProtocolSOCKS && l.SOCKS == nil:
				add(l.At.Field("protocols").Index(i), "socks needs the listener's socks, which says who may use it")
			}
			seen[p] = true
		}
		if l.TLS != nil && !ServesHTTP(l.Protocols) {
			add(l.At.Field("tls"), "tls serves the listener's HTTP, and this one serves none: tcp and udp are proxied as they are, TLS and all")
		}
		if l.SOCKS != nil {
			if !slices.Contains(l.Protocols, ProtocolSOCKS) {
				add(l.SOCKS.At, "socks is for a listener whose protocols include socks")
			}
			checkForwardProxy(l.SOCKS, add)
		}
		if pp := l.ProxyProtocol; pp != nil {
			if nets := l.networks(); len(nets) == 1 && nets[0] == "udp" {
				add(pp.At, "a PROXY protocol header comes at the start of a TCP connection; this listener takes UDP alone")
			}
			if len(pp.Trusted) == 0 {
				add(pp.At.Field("trusted"), "at least one address is needed")
			}
			for i, t := range pp.Trusted {
				if _, err := router.ParsePrefix(t); err != nil {
					add(pp.At.Field("trusted").Index(i), "%q is not an IP address or CIDR range", t)
				}
			}
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
			if ca := t.ClientAuth; ca != nil {
				if len(ca.CAFiles) == 0 {
					add(ca.At.Field("ca_files"), "at least one file is needed")
				}
				if ca.Mode != ClientAuthVerifyIfGiven && (len(t.ACME) > 0 || t.OnDemand || emptyTLS(t)) && !c.answersHTTP01() {
					add(ca.At, "a listener that requires client certificates cannot answer TLS-ALPN-01, the only challenge acme.challenges allows; set mode verify_if_given or allow http-01")
				}
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

	// l4 holds the listeners that serve tcp or udp, which services serve,
	// and noRoutes what serves the listeners that serve no HTTP.
	l4 := map[string]string{}
	noRoutes := map[string]string{}
	for _, l := range c.Listeners {
		for _, p := range [...]string{ProtocolTCP, ProtocolUDP} {
			if slices.Contains(l.Protocols, p) {
				l4[l.Name] = p
			}
		}
		switch {
		case ServesHTTP(l.Protocols):
		case l4[l.Name] != "":
			noRoutes[l.Name] = "a " + l4[l.Name] + " listener, which a service serves"
		default:
			noRoutes[l.Name] = "a socks listener"
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
			} else if what := noRoutes[name]; what != "" {
				add(r.At.Field("listeners").Index(i), "%q is %s, not routes: it serves no HTTP", name, what)
			}
		}
		if r.Match != nil {
			for i, h := range r.Match.Host {
				// "**." is a wildcard of one label or more, for routes alone.
				if rest, ok := strings.CutPrefix(h, "**."); ok {
					h = "*." + rest
				}
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
		if a.SharedStorage != nil {
			if a.Storage != "" {
				add(a.At.Field("storage"), "storage and shared_storage are two places to keep certificates: set one")
			}
			checkSharedStorage(a.SharedStorage, add)
		}
		for i, ch := range a.Challenges {
			if slices.Index(a.Challenges, ch) < i {
				add(a.At.Field("challenges").Index(i), "%q is listed twice", ch)
			}
		}
	}
	checkProviders(c.Providers, add)
	if m := c.Metrics; m != nil && c.Admin == nil {
		add(m.At, "the Admin API serves metrics: set admin too")
	}
	if t := c.Telemetry; t != nil {
		checkTelemetry(t, add)
	}
	if e := c.Engine; e != nil && e.Workers > 0 && e.MinWorkers > e.Workers {
		add(e.At.Field("min_workers"), "min_workers, %d, is more than workers, %d", e.MinWorkers, e.Workers)
	}

	upstreams := map[string]bool{}
	for _, u := range c.Upstreams {
		upstreams[u.Name] = true
	}
	services := map[string]bool{}
	servedBy := map[string]string{}
	// fallback is a tcp listener's service without server_names, and
	// byName the service of each listener and server name.
	fallback := map[string]string{}
	byName := map[[2]string]string{}
	for _, sv := range c.Services {
		switch {
		case !namePattern.MatchString(sv.Name):
			add(sv.At.Field("name"), "%q is not a valid name: use letters, digits, '_', '.' and '-', starting with a letter or digit", sv.Name)
		case services[sv.Name]:
			add(sv.At.Field("name"), "another service is already named %q", sv.Name)
		}
		services[sv.Name] = true
		if len(sv.Listeners) == 0 {
			add(sv.At.Field("listeners"), "a service needs at least one tcp or udp listener")
		}
		udp := false
		for i, name := range sv.Listeners {
			switch other, taken := servedBy[name]; {
			case !listeners[name]:
				add(sv.At.Field("listeners").Index(i), "there is no listener named %q", name)
			case l4[name] == "":
				add(sv.At.Field("listeners").Index(i), "%q is not a tcp or udp listener: a service takes the connections of listeners whose protocols include tcp, or are [\"udp\"]", name)
			case l4[name] == ProtocolUDP && taken:
				add(sv.At.Field("listeners").Index(i), "listener %q is already service %q's", name, other)
			case l4[name] == ProtocolUDP:
				udp = true
			case len(sv.ServerNames) == 0 && fallback[name] != "":
				add(sv.At.Field("listeners").Index(i), "listener %q is already service %q's: services that share a tcp listener take the connections of their server_names, and only one of them may have none", name, fallback[name])
			case len(sv.ServerNames) == 0:
				fallback[name] = sv.Name
			default:
				for j, sn := range sv.ServerNames {
					key := [2]string{name, strings.ToLower(sn)}
					if other, ok := byName[key]; ok && other != sv.Name {
						add(sv.At.Field("server_names").Index(j), "%q is already service %q's on listener %q", sn, other, name)
					}
					byName[key] = sv.Name
				}
			}
			servedBy[name] = sv.Name
		}
		seen := map[string]bool{}
		for i, sn := range sv.ServerNames {
			at := sv.At.Field("server_names").Index(i)
			switch msg := checkHost(sn); {
			case msg != "":
				add(at, "%s", msg)
			case net.ParseIP(sn) != nil:
				add(at, "%q is an IP address; a ClientHello names host names", sn)
			case seen[strings.ToLower(sn)]:
				add(at, "%q is listed twice", sn)
			}
			seen[strings.ToLower(sn)] = true
		}
		if udp && len(sv.ServerNames) > 0 {
			add(sv.At.Field("server_names"), "server_names pick a service by the TLS ClientHello of a tcp connection; a service with a udp listener takes none")
		}
		if udp && sv.ProxyProtocol != "" {
			add(sv.At.Field("proxy_protocol"), "the PROXY protocol is sent on tcp connections; a service with a udp listener sends none")
		}
		if !upstreams[sv.Upstream] {
			add(sv.At.Field("upstream"), "there is no upstream named %q", sv.Upstream)
		}
	}
	for _, l := range c.Listeners {
		if _, ok := servedBy[l.Name]; l4[l.Name] != "" && !ok {
			add(l.At.Field("protocols"), "no service takes this %s listener's connections: list it in a service's listeners", l4[l.Name])
		}
	}

	upstreams = map[string]bool{}
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
			if u.TLSCertFile != "" {
				add(u.At.Field("tls_cert_file"), "tls_cert_file needs protocol h2")
			}
		}
		switch {
		case u.TLSCertFile != "" && u.TLSKeyFile == "":
			add(u.At.Field("tls_cert_file"), "tls_cert_file needs tls_key_file")
		case u.TLSKeyFile != "" && u.TLSCertFile == "":
			add(u.At.Field("tls_key_file"), "tls_key_file needs tls_cert_file")
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

// answersHTTP01 reports whether certificates may be obtained through ACME's
// HTTP-01 challenge, which acme.challenges allows when left out.
func (c *Config) answersHTTP01() bool {
	return c.ACME == nil || len(c.ACME.Challenges) == 0 || slices.Contains(c.ACME.Challenges, ChallengeHTTP01)
}

// Check checks f, as Validate checks a listener's socks: for the params of
// a handler that takes the same rules.
func (f *ForwardProxy) Check() Errors {
	var errs Errors
	checkForwardProxy(f, func(v Value, format string, args ...any) { errs = append(errs, v.Errorf(format, args...)) })
	return errs
}

// checkForwardProxy checks who may use a forward proxy, and where to.
func checkForwardProxy(f *ForwardProxy, add func(Value, string, ...any)) {
	if len(f.Clients) == 0 {
		add(f.At.Field("clients"), "at least one address is needed")
	}
	for i, s := range f.Clients {
		if _, err := router.ParsePrefix(s); err != nil {
			add(f.At.Field("clients").Index(i), "%q is not an IP address or CIDR range", s)
		}
	}
	for _, field := range [...]string{"allow", "deny"} {
		dests := f.Allow
		if field == "deny" {
			dests = f.Deny
		}
		for i, d := range dests {
			if _, err := router.ParsePrefix(d); err == nil {
				continue
			}
			if msg := checkHost(d); msg != "" || net.ParseIP(strings.Trim(d, "[]")) != nil {
				add(f.At.Field(field).Index(i), "%q is not a host name, an IP address or a CIDR range", d)
			}
		}
	}
	for i, p := range f.Ports {
		if p < 1 || p > 65535 {
			add(f.At.Field("ports").Index(i), "%d is not a port", p)
		}
	}
}

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
