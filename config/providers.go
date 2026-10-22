package config

import (
	"net"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Provider is a source of config other than the file and the Admin API:
// what it provides is merged into the config, by the rules of
// docs/decisions/009-provider-merging.md. Exactly one of its kinds is set.
type Provider struct {
	Name   string          `json:"name,omitempty" doc:"The name the provider goes by in the Admin API's /config/effective and in logs: lower-case letters, digits, '_', '.' and '-', unique among the providers. Its kind, as docker, when left out."`
	Docker *DockerProvider `json:"docker,omitempty" doc:"Routes, upstreams and services from the labels of running Docker containers."`

	At Value `json:"-" fit:"self"`
}

// Kind returns which kind of provider p is, or "" when none is set.
func (p *Provider) Kind() string {
	if p.Docker != nil {
		return "docker"
	}
	return ""
}

// ID returns p's name, its kind when it has none.
func (p *Provider) ID() string {
	if p.Name != "" {
		return p.Name
	}
	return p.Kind()
}

// The defaults of DockerProvider.
const (
	DefaultDockerEndpoint      = "unix:///var/run/docker.sock"
	DefaultDockerLabelPrefix   = "fit"
	DefaultDockerMaxContainers = 1000
)

// The defaults of DockerProvider's durations.
var (
	DefaultDockerRefreshInterval = Duration(30 * time.Second)
	DefaultDockerRequestTimeout  = Duration(10 * time.Second)
)

// DockerProvider turns the labels of running containers into routes,
// upstreams and services. It follows the daemon's events, and lists the
// containers again at each change and every refresh_interval.
type DockerProvider struct {
	Endpoint          string   `json:"endpoint,omitempty" default:"\"unix:///var/run/docker.sock\"" doc:"Where the Docker daemon's API is: unix:// and a socket's path, or tcp:// and a host and port, as of a socket proxy, in plain HTTP. unix:///var/run/docker.sock when left out."`
	ExposeAll         bool     `json:"expose_all,omitempty" doc:"Take every running container with routes or services in its labels; only those labelled <label_prefix>.enable=true when left out or false. A container labelled <label_prefix>.enable=false is never taken."`
	Network           string   `json:"network,omitempty" doc:"The Docker network whose address of a container's is used, when the container is on several and has no <label_prefix>.network label; the first by name when left out."`
	UsePublishedPorts bool     `json:"use_published_ports,omitempty" doc:"Send to the port a container's port is published on, on the host, rather than to the container's own address: for a fit outside Docker that cannot reach containers' addresses, as on Docker Desktop."`
	LabelPrefix       string   `json:"label_prefix,omitempty" default:"\"fit\"" doc:"What the labels read start with, as in fit.routes.web.host. fit when left out."`
	MaxContainers     int      `json:"max_containers,omitempty" min:"0" default:"1000" doc:"The most containers taken; past it, the rest, by name, are left out and reported. 1000 when left out or 0."`
	RefreshInterval   Duration `json:"refresh_interval,omitempty" default:"\"30s\"" doc:"How often the containers are listed again whether or not an event came, in case one was missed. 30s when left out."`
	RequestTimeout    Duration `json:"request_timeout,omitempty" default:"\"10s\"" doc:"How long listing the containers may take, connecting included. 10s when left out."`

	At Value `json:"-" fit:"self"`
}

// Prefix, Containers, Refresh and Timeout return d's settings, their
// defaults when left out.
func (d *DockerProvider) Prefix() string {
	if d.LabelPrefix == "" {
		return DefaultDockerLabelPrefix
	}
	return d.LabelPrefix
}

func (d *DockerProvider) Containers() int {
	return int(orDefault(int64(d.MaxContainers), DefaultDockerMaxContainers))
}

func (d *DockerProvider) Refresh() time.Duration {
	if d.RefreshInterval <= 0 {
		return DefaultDockerRefreshInterval.Std()
	}
	return d.RefreshInterval.Std()
}

func (d *DockerProvider) Timeout() time.Duration {
	if d.RequestTimeout <= 0 {
		return DefaultDockerRequestTimeout.Std()
	}
	return d.RequestTimeout.Std()
}

// Dial returns the network and address the daemon's API is at.
func (d *DockerProvider) Dial() (network, address string) {
	ep := d.Endpoint
	if ep == "" {
		ep = DefaultDockerEndpoint
	}
	if path, ok := strings.CutPrefix(ep, "unix://"); ok {
		return "unix", path
	}
	if hostport, ok := strings.CutPrefix(ep, "tcp://"); ok {
		return "tcp", hostport
	}
	return "", ep
}

// providerPattern is what a provider's name looks like.
var providerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)

// baseSources are the names of the sources of the base config, which no
// provider may take.
var baseSources = []string{"file", "admin", "rollback", "certificates"}

func checkProviders(ps []Provider, add func(Value, string, ...any)) {
	seen := map[string]bool{}
	for _, p := range ps {
		kinds := 0
		if p.Docker != nil {
			kinds++
		}
		if kinds != 1 {
			add(p.At, "set exactly one kind of provider: docker")
			continue
		}
		id := p.ID()
		switch {
		case !providerPattern.MatchString(id):
			add(p.At.Field("name"), "%q is not a valid name: use lower-case letters, digits, '_', '.' and '-', starting with a letter or digit, at most 63", id)
		case slices.Contains(baseSources, id):
			add(p.At.Field("name"), "%q names a source of the base config: pick another name", id)
		case seen[id]:
			add(p.At.Field("name"), "another provider is already named %q: give one a name", id)
		}
		seen[id] = true
		if d := p.Docker; d != nil {
			network, address := d.Dial()
			switch {
			case network == "":
				add(d.At.Field("endpoint"), "%q is neither unix://<path> nor tcp://<host>:<port>", d.Endpoint)
			case network == "unix" && address == "":
				add(d.At.Field("endpoint"), "%q has no path", d.Endpoint)
			case network == "tcp":
				if _, _, err := net.SplitHostPort(address); err != nil {
					add(d.At.Field("endpoint"), "%q has no host and port", d.Endpoint)
				}
			}
			if d.LabelPrefix != "" && !namePattern.MatchString(d.LabelPrefix) {
				add(d.At.Field("label_prefix"), "%q is not a valid prefix: use letters, digits, '_', '.' and '-', starting with a letter or digit", d.LabelPrefix)
			}
		}
	}
}
