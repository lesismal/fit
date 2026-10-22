package docker

import (
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/provider/labels"
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

// translate turns containers into a part, and says why it left out the
// containers it did. The part is the same for the same containers, so
// that an unchanged one is seen to be.
func translate(cs []container, opts *config.DockerProvider) (part []byte, problems []string) {
	prefix := opts.Prefix() + "."
	slices.SortFunc(cs, func(a, b container) int { return strings.Compare(a.name(), b.name()) })
	groups := map[string]*labels.Group{}
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
		if !labels.NamePattern.MatchString(name) {
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
		addr, err := address(c, l.Port, network, opts.UsePublishedPorts)
		if err != nil {
			problem("%v", err)
			continue
		}
		g := groups[name]
		if g == nil {
			g = &labels.Group{Set: l, SvcAddrs: map[string][]string{}}
			groups[name] = g
		}
		svcAddrs := map[string]string{}
		for key, s := range g.Set.Services {
			if s.Port() == 0 {
				continue
			}
			a, err := address(c, s.Port(), network, opts.UsePublishedPorts)
			if err != nil {
				problem("service %s: %v", key, err)
				continue
			}
			svcAddrs[key] = a
		}
		if len(svcAddrs) != g.Set.Ports() {
			continue
		}
		g.Addrs = append(g.Addrs, addr)
		for key, a := range svcAddrs {
			g.SvcAddrs[key] = append(g.SvcAddrs[key], a)
		}
		taken++
	}
	return labels.Part(groups), problems
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
func parseLabels(c *container, prefix string) (*labels.Set, error) {
	l, err := labels.Parse(c.Labels, prefix, []string{"enable", "name", "network"}, labels.Kind{Label: "label", Owner: "container"})
	if err != nil {
		return nil, err
	}
	if l.Port == 0 {
		var ports []int
		for _, p := range c.Ports {
			if p.Type == "tcp" && !slices.Contains(ports, p.PrivatePort) {
				ports = append(ports, p.PrivatePort)
			}
		}
		switch len(ports) {
		case 1:
			l.Port = ports[0]
		case 0:
			return nil, fmt.Errorf("it exposes no TCP port: set %sport", prefix)
		default:
			slices.Sort(ports)
			return nil, fmt.Errorf("it exposes TCP ports %v: set %sport", ports, prefix)
		}
	}
	return l, nil
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
	names := labels.SortedKeys(nets)
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
