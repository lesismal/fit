package provider

import (
	"encoding/json"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/config"
)

// Names are what a provider's part may refer to, or must keep clear of,
// besides its own elements: the listeners, upstreams, services and routes
// of the base config and of the other providers' parts, as the running
// config has them. A part that names a listener there is not, or claims
// what another source has, would be refused as a whole, and with it every
// change the provider makes for anyone; one whose upstream's name another
// source has would have its routes send to that source's upstream.
type Names struct {
	Listeners map[string]Listener
	Upstreams map[string]bool
	Routes    map[string]bool
	Services  []Service
}

// Listener is a listener of another source's.
type Listener struct {
	HTTP bool   // it serves HTTP, as routes take
	L4   string // tcp or udp, as services take; "" for neither
	Host string // the host its address binds; "" for every one
	Port int
}

// Service is a service of another source's.
type Service struct {
	Name        string
	Listeners   []string
	ServerNames []string
}

// An Aware provider is told the Names of the running config before it
// runs, as a function it calls each time it makes a part.
type Aware interface {
	SetNames(names func() *Names)
}

// NamesOf reads the Names of data, a running config as JSON (or JSON with
// comments), whose elements came from sources, leaving out those provider
// provided. It returns nil when data cannot be read.
func NamesOf(data []byte, sources []Source, provider string) *Names {
	std, err := config.Standardize(data)
	if err != nil {
		return nil
	}
	var c struct {
		Listeners []struct {
			Name      string   `json:"name"`
			Address   string   `json:"address"`
			Protocols []string `json:"protocols"`
		} `json:"listeners"`
		Upstreams []struct {
			Name string `json:"name"`
		} `json:"upstreams"`
		Routes []struct {
			Name string `json:"name"`
		} `json:"routes"`
		Services []struct {
			Name        string   `json:"name"`
			Listeners   []string `json:"listeners"`
			ServerNames []string `json:"server_names"`
		} `json:"services"`
	}
	if json.Unmarshal(std, &c) != nil {
		return nil
	}
	own := map[string]bool{}
	for _, s := range sources {
		if s.Source == provider {
			own[s.Path] = true
		}
	}
	n := &Names{Listeners: map[string]Listener{}, Upstreams: map[string]bool{}, Routes: map[string]bool{}}
	for i, l := range c.Listeners {
		if own[pointer("listeners", i)] {
			continue
		}
		nl := Listener{HTTP: config.ServesHTTP(l.Protocols)}
		for _, p := range [...]string{config.ProtocolTCP, config.ProtocolUDP} {
			if slices.Contains(l.Protocols, p) {
				nl.L4 = p
			}
		}
		if host, port, err := net.SplitHostPort(l.Address); err == nil {
			nl.Host = strings.Trim(host, "[]")
			nl.Port, _ = strconv.Atoi(port)
		}
		n.Listeners[l.Name] = nl
	}
	for i, u := range c.Upstreams {
		if !own[pointer("upstreams", i)] {
			n.Upstreams[u.Name] = true
		}
	}
	for i, r := range c.Routes {
		if !own[pointer("routes", i)] && r.Name != "" {
			n.Routes[r.Name] = true
		}
	}
	for i, s := range c.Services {
		if !own[pointer("services", i)] {
			n.Services = append(n.Services, Service{Name: s.Name, Listeners: s.Listeners, ServerNames: s.ServerNames})
		}
	}
	return n
}

// Binds reports whether a listener of the Names binds port, of protocol
// udp or not, where one on host would: on the same host, or either on
// every one.
func (n *Names) Binds(host string, port int, udp bool) (string, bool) {
	if n == nil {
		return "", false
	}
	for name, l := range n.Listeners {
		if l.Port != port || (l.L4 == config.ProtocolUDP) != udp {
			continue
		}
		if l.Host == "" || host == "" || unspecified(l.Host) || unspecified(host) || strings.EqualFold(l.Host, host) {
			return name, true
		}
	}
	return "", false
}

func unspecified(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}
