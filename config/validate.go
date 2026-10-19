package config

import (
	"net"
	"regexp"
	"strconv"
	"strings"
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

	if len(c.Listeners) == 0 {
		errs = append(errs, &Error{Path: "listeners", Msg: "at least one listener is needed"})
	}
	listeners := map[string]bool{}
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
			if seen[p] {
				add(l.At.Field("protocols").Index(i), "%q is listed twice", p)
			}
			seen[p] = true
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
		if u.Protocol != "h2" {
			if u.TLSServerName != "" {
				add(u.At.Field("tls_server_name"), "tls_server_name needs protocol h2")
			}
			if u.TLSCAFile != "" {
				add(u.At.Field("tls_ca_file"), "tls_ca_file needs protocol h2")
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
