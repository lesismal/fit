package config

import (
	"net"
	"slices"
	"strconv"
	"strings"
)

// The defaults of AutoHTTPS.
const (
	DefaultHTTPPort  = 80
	DefaultHTTPSPort = 443
)

// The names of the listeners HTTPS by default makes.
const (
	AutoHTTPListener  = "http"
	AutoHTTPSListener = "https"
)

// AutoHTTPS is HTTPS by default: a config that writes no listeners is served
// over HTTPS, with certificates obtained with ACME for its routes' host
// names, and over HTTP only to be redirected; and a listener whose tls is
// empty gets certificates for the host names of the routes it serves.
// Listeners written out are served as written.
type AutoHTTPS struct {
	Disable          bool `json:"disable,omitempty" doc:"Turn HTTPS by default off: no listeners are made for a config that writes none, and an empty tls gets no certificates."`
	DisableRedirects bool `json:"disable_redirects,omitempty" doc:"Make no listener in the clear to redirect to HTTPS. ACME's HTTP-01 challenge is then answered by none, and certificates come through TLS-ALPN-01."`
	HTTPPort         int  `json:"http_port,omitempty" min:"0" max:"65535" default:"80" doc:"The port of the listener in the clear that redirects to HTTPS. 80 when left out or 0."`
	HTTPSPort        int  `json:"https_port,omitempty" min:"0" max:"65535" default:"443" doc:"The port of the HTTPS listener, and the one redirects name when it is not 443. 443 when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// HTTP and HTTPS return a's ports, their defaults when a is nil or they are
// left out.
func (a *AutoHTTPS) HTTP() int {
	if a == nil || a.HTTPPort <= 0 {
		return DefaultHTTPPort
	}
	return a.HTTPPort
}

func (a *AutoHTTPS) HTTPS() int {
	if a == nil || a.HTTPSPort <= 0 {
		return DefaultHTTPSPort
	}
	return a.HTTPSPort
}

// on reports whether HTTPS by default is on, and redirects whether it makes
// a listener in the clear to redirect to HTTPS.
func (a *AutoHTTPS) on() bool        { return a == nil || !a.Disable }
func (a *AutoHTTPS) redirects() bool { return a == nil || !a.DisableRedirects }

// privateSuffixes are the names, and the parents of names, that only a
// private network resolves, which no public CA gives a certificate for.
var privateSuffixes = []string{"localhost", "local", "internal", "home.arpa", "invalid"}

// AutoHTTPSName reports whether HTTPS by default obtains a certificate for
// host: a host name of two labels or more, and not a wildcard, an IP address
// or a name only a private network resolves.
func AutoHTTPSName(host string) bool {
	name := strings.ToLower(strings.TrimSuffix(host, "."))
	if strings.HasPrefix(name, "*.") || net.ParseIP(strings.Trim(name, "[]")) != nil || !strings.Contains(name, ".") {
		return false
	}
	for _, s := range privateSuffixes {
		if name == s || strings.HasSuffix(name, "."+s) {
			return false
		}
	}
	return true
}

// autoNames returns the host names HTTPS by default obtains certificates
// for among the routes listener serves, or every route's when listener is
// empty; in lower case, each once, in the order written.
func (c *Config) autoNames(listener string) []string {
	var names []string
	for _, r := range c.Routes {
		if r.Match == nil || listener != "" && len(r.Listeners) > 0 && !slices.Contains(r.Listeners, listener) {
			continue
		}
		for _, h := range r.Match.Host {
			h = strings.ToLower(strings.TrimSuffix(h, "."))
			if AutoHTTPSName(h) && !slices.Contains(names, h) {
				names = append(names, h)
			}
		}
	}
	return names
}

// emptyTLS reports whether t asks for nothing: no certificates, no names
// and no on-demand TLS.
func emptyTLS(t *TLS) bool {
	return t != nil && len(t.Certificates) == 0 && len(t.ACME) == 0 && !t.OnDemand
}

// Effective returns the listeners c is served on. Those written are
// returned as written, but for one whose tls is empty, which gets its
// routes' host names as tls.acme while HTTPS by default is on. A config
// that writes none gets, while it is on, an https listener whose tls.acme
// is every route's host names and, unless redirects are off, an http
// listener that redirects to it: redirect holds the HTTPS port each
// redirecting listener redirects to, by name. c is not changed.
func (c *Config) Effective() (listeners []Listener, redirect map[string]int) {
	auto := c.AutoHTTPS
	if len(c.Listeners) > 0 {
		listeners = slices.Clone(c.Listeners)
		if auto.on() {
			for i, l := range listeners {
				if emptyTLS(l.TLS) {
					t := *l.TLS
					t.ACME = c.autoNames(l.Name)
					listeners[i].TLS = &t
				}
			}
		}
		return listeners, nil
	}
	if !auto.on() {
		return nil, nil
	}
	names := c.autoNames("")
	if len(names) == 0 {
		return nil, nil
	}
	https := strconv.Itoa(auto.HTTPS())
	listeners = append(listeners, Listener{Name: AutoHTTPSListener, Address: ":" + https, TLS: &TLS{ACME: names}})
	if auto.redirects() {
		listeners = append(listeners, Listener{Name: AutoHTTPListener, Address: ":" + strconv.Itoa(auto.HTTP())})
		redirect = map[string]int{AutoHTTPListener: auto.HTTPS()}
	}
	return listeners, redirect
}
