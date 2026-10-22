package docker

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/config"
)

// container is what the daemon's list says of a running container.
type container struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
	Labels map[string]string `json:"Labels"`
	Ports  []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress         string `json:"IPAddress"`
			GlobalIPv6Address string `json:"GlobalIPv6Address"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

func (c *container) name() string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return c.ID
}

var (
	// namePattern is what the config takes as a name.
	namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	// keyPattern is what a route's or a service's key in a label looks
	// like: a name without dots, which separate a label's parts.
	keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
)

// group is the containers of one name: one upstream, and the routes and
// services the first of them writes.
type group struct {
	name     string
	labels   *labels
	addrs    []string
	svcAddrs map[string][]string // the addresses of the services with a port of their own
}

// labels is what a container's labels say.
type labels struct {
	port     int
	upstream map[string]json.RawMessage
	routes   map[string]*route
	services map[string]*service
}

type route struct {
	Name      string          `json:"name"`
	Listeners []string        `json:"listeners,omitempty"`
	Priority  int             `json:"priority,omitempty"`
	Match     *match          `json:"match,omitempty"`
	Middle    json.RawMessage `json:"middleware,omitempty"`
	Handler   json.RawMessage `json:"handler"`
}

type match struct {
	Host       []string `json:"host,omitempty"`
	Path       []string `json:"path,omitempty"`
	PathRegexp []string `json:"path_regexp,omitempty"`
	Method     []string `json:"method,omitempty"`
	Expr       string   `json:"expr,omitempty"`
}

type service struct {
	Name          string   `json:"name"`
	Listeners     []string `json:"listeners"`
	Upstream      string   `json:"upstream"`
	ServerNames   []string `json:"server_names,omitempty"`
	ProxyProtocol string   `json:"proxy_protocol,omitempty"`

	port int
}

// translate turns containers into a part, and says why it left out the
// containers it did. The part is the same for the same containers, so
// that an unchanged one is seen to be.
func translate(cs []container, opts *config.DockerProvider) (part []byte, problems []string) {
	prefix := opts.Prefix() + "."
	slices.SortFunc(cs, func(a, b container) int { return strings.Compare(a.name(), b.name()) })
	groups := map[string]*group{}
	taken := 0
	for i := range cs {
		c := &cs[i]
		problem := func(format string, args ...any) {
			problems = append(problems, fmt.Sprintf("container %s: ", c.name())+fmt.Sprintf(format, args...))
		}
		switch enable := c.Labels[prefix+"enable"]; {
		case enable == "false", !opts.ExposeAll && enable != "true":
			continue
		case c.State != "" && c.State != "running":
			continue
		case strings.Contains(c.Status, "(unhealthy)"), strings.Contains(c.Status, "(health: starting)"):
			continue // back once it is healthy
		}
		if taken == opts.Containers() {
			problem("left out: max_containers, %d, are taken", opts.Containers())
			continue
		}
		name := groupName(c, prefix)
		if !namePattern.MatchString(name) {
			problem("%q is not a valid name for an upstream: set %sname", name, prefix)
			continue
		}
		l, err := parseLabels(c, prefix)
		if err != nil {
			problem("%v", err)
			continue
		}
		network := c.Labels[prefix+"network"]
		if network == "" {
			network = opts.Network
		}
		addr, err := address(c, l.port, network, opts.UsePublishedPorts)
		if err != nil {
			problem("%v", err)
			continue
		}
		g := groups[name]
		if g == nil {
			g = &group{name: name, labels: l, svcAddrs: map[string][]string{}}
			groups[name] = g
		}
		svcAddrs := map[string]string{}
		for key, s := range g.labels.services {
			if s.port == 0 {
				continue
			}
			a, err := address(c, s.port, network, opts.UsePublishedPorts)
			if err != nil {
				problem("service %s: %v", key, err)
				continue
			}
			svcAddrs[key] = a
		}
		if len(svcAddrs) != countPorts(g.labels.services) {
			continue
		}
		g.addrs = append(g.addrs, addr)
		for key, a := range svcAddrs {
			g.svcAddrs[key] = append(g.svcAddrs[key], a)
		}
		taken++
	}

	var out struct {
		Upstreams []map[string]json.RawMessage `json:"upstreams,omitempty"`
		Routes    []*route                     `json:"routes,omitempty"`
		Services  []*service                   `json:"services,omitempty"`
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		g := groups[name]
		out.Upstreams = append(out.Upstreams, upstream(name, g.addrs, g.labels.upstream))
		for _, key := range sortedKeys(g.labels.routes) {
			r := *g.labels.routes[key]
			r.Name = name + "." + key
			if r.Handler == nil {
				r.Handler, _ = json.Marshal(map[string]any{"module": "proxy", "params": map[string]string{"upstream": name}})
			}
			out.Routes = append(out.Routes, &r)
		}
		for _, key := range sortedKeys(g.labels.services) {
			s := *g.labels.services[key]
			s.Name = name + "." + key
			s.Upstream = name
			if s.port != 0 {
				s.Upstream = s.Name
				out.Upstreams = append(out.Upstreams, upstream(s.Name, g.svcAddrs[key], nil))
			}
			out.Services = append(out.Services, &s)
		}
	}
	part, _ = json.Marshal(out)
	return part, problems
}

func countPorts(ss map[string]*service) int {
	n := 0
	for _, s := range ss {
		if s.port != 0 {
			n++
		}
	}
	return n
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// upstream is an upstream of name, sending to addrs, with the other
// settings extra gives.
func upstream(name string, addrs []string, extra map[string]json.RawMessage) map[string]json.RawMessage {
	u := make(map[string]json.RawMessage, len(extra)+2)
	for k, v := range extra {
		u[k] = v
	}
	u["name"], _ = json.Marshal(name)
	u["addresses"], _ = json.Marshal(addrs)
	return u
}

// groupName is the name of c's upstream: its name label, or its Compose
// project and service, or its own name.
func groupName(c *container, prefix string) string {
	if name := c.Labels[prefix+"name"]; name != "" {
		return name
	}
	project, service := c.Labels["com.docker.compose.project"], c.Labels["com.docker.compose.service"]
	if project != "" && service != "" {
		return project + "-" + service
	}
	return c.name()
}

// parseLabels reads c's labels under prefix.
func parseLabels(c *container, prefix string) (*labels, error) {
	l := &labels{routes: map[string]*route{}, services: map[string]*service{}}
	for _, key := range sortedKeys(c.Labels) {
		rest, ok := strings.CutPrefix(key, prefix)
		if !ok {
			continue
		}
		value := c.Labels[key]
		bad := func(format string, args ...any) error {
			return fmt.Errorf("label %s: "+format, append([]any{key}, args...)...)
		}
		switch {
		case rest == "enable" || rest == "name" || rest == "network":
		case rest == "port":
			p, err := parsePort(value)
			if err != nil {
				return nil, bad("%v", err)
			}
			l.port = p
		case rest == "upstream":
			if err := json.Unmarshal([]byte(value), &l.upstream); err != nil || l.upstream == nil {
				return nil, bad("must be a JSON object of an upstream's settings")
			}
			if _, ok := l.upstream["name"]; ok {
				return nil, bad("name is the container's: set %sname", prefix)
			}
			if _, ok := l.upstream["addresses"]; ok {
				return nil, bad("addresses are the containers'")
			}
		case strings.HasPrefix(rest, "routes."):
			k, field, ok := strings.Cut(strings.TrimPrefix(rest, "routes."), ".")
			if !ok || !keyPattern.MatchString(k) {
				return nil, bad("is not %sroutes.<name>.<setting>, with a name of letters, digits, '_' and '-'", prefix)
			}
			r := l.routes[k]
			if r == nil {
				r = &route{}
				l.routes[k] = r
			}
			if err := r.set(field, value); err != nil {
				return nil, bad("%v", err)
			}
		case strings.HasPrefix(rest, "services."):
			k, field, ok := strings.Cut(strings.TrimPrefix(rest, "services."), ".")
			if !ok || !keyPattern.MatchString(k) {
				return nil, bad("is not %sservices.<name>.<setting>, with a name of letters, digits, '_' and '-'", prefix)
			}
			s := l.services[k]
			if s == nil {
				s = &service{}
				l.services[k] = s
			}
			if err := s.set(field, value); err != nil {
				return nil, bad("%v", err)
			}
		default:
			return nil, bad("is not a label fit reads")
		}
	}
	for k, s := range l.services {
		if len(s.Listeners) == 0 {
			return nil, fmt.Errorf("service %s: set %sservices.%s.listeners", k, prefix, k)
		}
	}
	if l.port == 0 {
		var ports []int
		for _, p := range c.Ports {
			if p.Type == "tcp" && !slices.Contains(ports, p.PrivatePort) {
				ports = append(ports, p.PrivatePort)
			}
		}
		switch len(ports) {
		case 1:
			l.port = ports[0]
		case 0:
			return nil, fmt.Errorf("it exposes no TCP port: set %sport", prefix)
		default:
			slices.Sort(ports)
			return nil, fmt.Errorf("it exposes TCP ports %v: set %sport", ports, prefix)
		}
	}
	return l, nil
}

func (r *route) set(field, value string) error {
	m := r.Match
	if m == nil {
		m = &match{}
	}
	switch field {
	case "host":
		m.Host = list(value)
	case "path":
		m.Path = list(value)
	case "path_regexp":
		m.PathRegexp = list(value)
	case "method":
		m.Method = list(value)
	case "expr":
		m.Expr = value
	case "priority":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%q is not a whole number", value)
		}
		r.Priority = n
	case "listeners":
		r.Listeners = list(value)
	case "middleware":
		var a []json.RawMessage
		if err := json.Unmarshal([]byte(value), &a); err != nil {
			return fmt.Errorf("must be a JSON array of middleware, as in the config")
		}
		r.Middle = json.RawMessage(value)
	case "handler":
		var o map[string]json.RawMessage
		if err := json.Unmarshal([]byte(value), &o); err != nil || o == nil {
			return fmt.Errorf("must be a JSON object of a handler, as in the config")
		}
		r.Handler = json.RawMessage(value)
	default:
		return fmt.Errorf("a route has no setting %q", field)
	}
	if m.Host != nil || m.Path != nil || m.PathRegexp != nil || m.Method != nil || m.Expr != "" {
		r.Match = m
	}
	return nil
}

func (s *service) set(field, value string) error {
	switch field {
	case "listeners":
		s.Listeners = list(value)
	case "port":
		p, err := parsePort(value)
		if err != nil {
			return err
		}
		s.port = p
	case "server_names":
		s.ServerNames = list(value)
	case "proxy_protocol":
		s.ProxyProtocol = value
	default:
		return fmt.Errorf("a service has no setting %q", field)
	}
	return nil
}

func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("%q is not a port", s)
	}
	return p, nil
}

// list splits a comma-separated value, leaving out empty entries.
func list(s string) []string {
	var out []string
	for v := range strings.SplitSeq(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// address is where c's port is reached: its address on network (or its
// only network, or the first by name), the host's loopback for a container
// on the host's network, or, with published, the host port it is published
// on.
func address(c *container, port int, network string, published bool) (string, error) {
	if published {
		for _, p := range c.Ports {
			if p.Type != "tcp" || p.PrivatePort != port || p.PublicPort == 0 {
				continue
			}
			ip := p.IP
			switch ip {
			case "", "0.0.0.0":
				ip = "127.0.0.1"
			case "::":
				ip = "::1"
			}
			return net.JoinHostPort(ip, strconv.Itoa(p.PublicPort)), nil
		}
		return "", fmt.Errorf("port %d is not published on the host", port)
	}
	if c.HostConfig.NetworkMode == "host" {
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil
	}
	nets := c.NetworkSettings.Networks
	if network != "" {
		n, ok := nets[network]
		if !ok {
			return "", fmt.Errorf("it is not on network %q", network)
		}
		return joinIP(n.IPAddress, n.GlobalIPv6Address, port, network)
	}
	names := sortedKeys(nets)
	for _, name := range names {
		n := nets[name]
		if a, err := joinIP(n.IPAddress, n.GlobalIPv6Address, port, name); err == nil {
			return a, nil
		}
	}
	return "", fmt.Errorf("it has no address on any network")
}

func joinIP(v4, v6 string, port int, network string) (string, error) {
	ip := v4
	if ip == "" {
		ip = v6
	}
	if ip == "" {
		return "", fmt.Errorf("it has no address on network %q", network)
	}
	return net.JoinHostPort(ip, strconv.Itoa(port)), nil
}
