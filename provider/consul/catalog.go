package consul

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/apiclient"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/provider/internal/watch"
	"github.com/lesismal/fit/provider/labels"
)

// Catalog is the Consul catalog provider. Each service taken is an
// upstream of its instances whose health checks pass, named for the
// service or its <prefix>.name tag; its instances' tags of the form
// <prefix>.<key>=<value> are labels, as a container's are for the Docker
// provider (see package labels), and the first instance by node and ID
// writes the routes and services. Tags without '=' are passed over.
type Catalog struct {
	name string
	f    *watch.Follower
}

// NewCatalog returns a Consul catalog provider named name, with the
// settings opts. log, which may be nil, hears what the provider reports:
// instances left out and Consul failing.
func NewCatalog(name string, opts *config.ConsulProvider, log *logs.Logger) *Catalog {
	src := &catalogSource{opts: opts, conn: conn{
		endpoint:   opts.URL(),
		datacenter: opts.Datacenter,
		tokenFile:  opts.TokenFile,
		files:      apiclient.TLSFiles{CA: opts.CAFile, Cert: opts.CertFile, Key: opts.KeyFile},
		timeout:    opts.Timeout(),
	}}
	return &Catalog{name: name, f: &watch.Follower{Name: name, Interval: opts.Refresh(), Log: log, Source: src}}
}

// Name returns the provider's name.
func (p *Catalog) Name() string { return p.name }

// Run follows the catalog until ctx ends, calling update with what its
// services provide: at once, after each change to the catalog or to a
// health check, and every refresh_interval whether or not it changed.
// When Consul cannot be reached, the last part stays, and Consul is tried
// again, from 1s to 30s apart.
func (p *Catalog) Run(ctx context.Context, update func([]byte)) { p.f.Run(ctx, update) }

type catalogSource struct {
	conn
	opts *config.ConsulProvider
	c    *client // the watch's
}

func (s *catalogSource) Open(context.Context) (func(), error) {
	c, err := s.conn.open()
	if err != nil {
		return nil, err
	}
	s.c = c
	return c.close, nil
}

// Watch polls the catalog's services and the health checks: a service
// registered or gone changes the one, an instance passing or failing its
// checks the other.
func (s *catalogSource) Watch(ctx context.Context, ready, changed func()) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	paths := []string{"/v1/catalog/services", "/v1/health/state/any"}
	errs := make(chan error, len(paths))
	var mu sync.Mutex
	waiting := len(paths)
	for _, path := range paths {
		go func() {
			errs <- s.c.poll(ctx, path, nil, func() {
				mu.Lock()
				waiting--
				all := waiting == 0
				mu.Unlock()
				if all {
					ready()
				}
			}, changed)
		}()
	}
	err := <-errs
	cancel()
	for range len(paths) - 1 {
		<-errs
	}
	return err
}

// entry is what /v1/health/service says of an instance.
type entry struct {
	Node struct {
		Node    string `json:"Node"`
		Address string `json:"Address"`
	} `json:"Node"`
	Service struct {
		ID      string   `json:"ID"`
		Service string   `json:"Service"`
		Tags    []string `json:"Tags"`
		Address string   `json:"Address"`
		Port    int      `json:"Port"`
	} `json:"Service"`
}

// Read lists the catalog's services, and the instances of those taken
// whose checks pass.
func (s *catalogSource) Read(ctx context.Context) ([]byte, []string, error) {
	data, _, err := s.c.get(ctx, "/v1/catalog/services", nil, 0)
	if err != nil {
		return nil, nil, err
	}
	var services map[string][]string
	if err := json.Unmarshal(data, &services); err != nil {
		return nil, nil, fmt.Errorf("GET /v1/catalog/services: %w", err)
	}
	prefix := s.opts.Prefix() + "."
	var problems []string
	instances := map[string][]entry{}
	taken := 0
	for _, name := range labels.SortedKeys(services) {
		if !s.opts.ExposeAll && !slices.Contains(services[name], prefix+"enable=true") {
			continue
		}
		if taken == s.opts.Services() {
			problems = append(problems, fmt.Sprintf("service %s: left out: max_services, %d, are taken", name, s.opts.Services()))
			continue
		}
		taken++
		path := "/v1/health/service/" + url.PathEscape(name)
		data, _, err := s.c.get(ctx, path, url.Values{"passing": {"1"}}, 0)
		if err != nil {
			return nil, nil, err
		}
		var es []entry
		if err := json.Unmarshal(data, &es); err != nil {
			return nil, nil, fmt.Errorf("GET %s: %w", path, err)
		}
		instances[name] = es
	}
	part, more := translate(instances, s.opts)
	return part, append(problems, more...), nil
}

// translate turns the services' instances into a part, and says why it
// left out the instances it did. The part is the same for the same
// instances, so that an unchanged one is seen to be.
func translate(services map[string][]entry, opts *config.ConsulProvider) (part []byte, problems []string) {
	prefix := opts.Prefix() + "."
	groups := map[string]*labels.Group{}
	for _, svc := range labels.SortedKeys(services) {
		es := services[svc]
		slices.SortFunc(es, func(a, b entry) int {
			return cmp.Or(strings.Compare(a.Node.Node, b.Node.Node), strings.Compare(a.Service.ID, b.Service.ID))
		})
		for i := range es {
			e := &es[i]
			problem := func(format string, args ...any) {
				problems = append(problems, fmt.Sprintf("service %s, instance %s on node %s: ", svc, e.Service.ID, e.Node.Node)+fmt.Sprintf(format, args...))
			}
			tags, err := tagLabels(e.Service.Tags, prefix)
			if err != nil {
				problem("%v", err)
				continue
			}
			if enable := tags[prefix+"enable"]; enable == "false" || !opts.ExposeAll && enable != "true" {
				continue
			}
			name := tags[prefix+"name"]
			if name == "" {
				name = svc
			}
			if !labels.NamePattern.MatchString(name) {
				problem("%q is not a valid name for an upstream: tag it %sname=<name>", name, prefix)
				continue
			}
			l, err := labels.Parse(tags, prefix, []string{"enable", "name"}, labels.Kind{Label: "tag", Owner: "service"})
			if err != nil {
				problem("%v", err)
				continue
			}
			host := e.Service.Address
			if host == "" {
				host = e.Node.Address
			}
			if host == "" {
				problem("it has no address, nor has its node")
				continue
			}
			port := l.Port
			if port == 0 {
				port = e.Service.Port
			}
			if port == 0 {
				problem("it has no port: tag it %sport=<port>", prefix)
				continue
			}
			g := groups[name]
			if g == nil {
				g = &labels.Group{Set: l, SvcAddrs: map[string][]string{}}
				groups[name] = g
			}
			g.Addrs = append(g.Addrs, net.JoinHostPort(host, strconv.Itoa(port)))
			for key, sv := range g.Set.Services {
				if p := sv.Port(); p != 0 {
					g.SvcAddrs[key] = append(g.SvcAddrs[key], net.JoinHostPort(host, strconv.Itoa(p)))
				}
			}
		}
	}
	return labels.Part(groups), problems
}

// tagLabels reads the tags under prefix as labels, key=value.
func tagLabels(tags []string, prefix string) (map[string]string, error) {
	m := map[string]string{}
	for _, t := range tags {
		if !strings.HasPrefix(t, prefix) {
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		if !ok {
			return nil, errors.New("tag " + t + ": is not <key>=<value>")
		}
		m[k] = v
	}
	return m, nil
}
