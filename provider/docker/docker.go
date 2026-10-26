// Package docker is the Docker provider: it turns the labels of running
// containers into routes, upstreams and services, and follows the daemon's
// events to keep them up to date.
//
// It speaks the Engine API over plain HTTP, on a Unix socket or TCP, with
// unversioned paths; see docs/decisions/010-docker-api-over-http.md. It runs
// in the control plane, on goroutines of its own.
//
// The labels, under a prefix (fit by default):
//
//	fit.enable=true                 take the container (needed unless expose_all)
//	fit.name=<name>                 the upstream's name, shared by replicas; the
//	                                Compose project and service ("<project>-<service>")
//	                                or the container's name when left out
//	fit.port=<port>                 the container's port; its only TCP port when left out
//	fit.network=<network>           the network whose address is used
//	fit.upstream=<JSON object>      more of the upstream's settings, as in the config
//	fit.routes.<r>.host=<a,b>       a route named <name>.<r>, matching these hosts;
//	fit.routes.<r>.path=<a,b>       ... paths, as in the config's match
//	fit.routes.<r>.path_regexp=<a,b>
//	fit.routes.<r>.method=<a,b>
//	fit.routes.<r>.expr=<expr>
//	fit.routes.<r>.priority=<n>
//	fit.routes.<r>.listeners=<a,b>
//	fit.routes.<r>.middleware=<JSON array>
//	fit.routes.<r>.handler=<JSON object>  proxy to the upstream when left out
//	fit.services.<s>.listeners=<a,b>      a layer 4 service named <name>.<s>
//	fit.services.<s>.port=<port>          to this port, through an upstream of its own
//	fit.services.<s>.server_names=<a,b>
//	fit.services.<s>.proxy_protocol=<v1|v2>
//
// Replicas, the containers of one name, add their addresses to one upstream;
// its routes and services are read from the first by container name.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// Bounds on what is read from the daemon.
const (
	maxListBytes  = 16 << 20
	maxEventBytes = 1 << 20
	maxErrorBytes = 4 << 10
)

// Timings, variables for tests to shorten.
var (
	// debounce is how long the provider waits after an event for more,
	// before listing the containers again.
	debounce = 200 * time.Millisecond
	// minBackoff and maxBackoff bound the wait before trying the daemon
	// again after it failed.
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
)

// Provider is a Docker provider.
type Provider struct {
	name string
	opts *config.DockerProvider
	c    *client
	log  *logs.Logger

	last     []byte          // the part sent last
	problems map[string]bool // the problems reported at the last listing
}

// New returns a Docker provider named name, with the settings opts. log,
// which may be nil, hears what the provider reports: containers left out
// and the daemon failing.
func New(name string, opts *config.DockerProvider, log *logs.Logger) *Provider {
	return &Provider{name: name, opts: opts, c: newClient(opts), log: log}
}

// Name returns the provider's name.
func (p *Provider) Name() string { return p.name }

// Run follows the daemon until ctx ends, calling update with what the
// containers provide: at once, after each change, and every
// refresh_interval whether or not it changed, so that a part refused
// because of the rest of the config is tried again. When the daemon cannot
// be reached, the last part stays, and the daemon is tried again, from 1s
// to 30s apart.
func (p *Provider) Run(ctx context.Context, update func([]byte)) {
	backoff := minBackoff
	for {
		listed, err := p.watch(ctx, update)
		if ctx.Err() != nil {
			return
		}
		if listed {
			backoff = minBackoff
		}
		p.log.Warnf("provider %s: %v; trying again in %s", p.name, err, backoff)
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		backoff = min(2*backoff, maxBackoff)
	}
}

// watch follows the daemon's events, and lists the containers at the
// start, after events and every refresh_interval, until the daemon fails or
// ctx ends. It reports whether a listing succeeded.
func (p *Provider) watch(ctx context.Context, update func([]byte)) (listed bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The events first, so that no change between the listing and them
	// is missed.
	events, err := p.c.events(ctx)
	if err != nil {
		return false, err
	}
	defer events.Close()
	changed := make(chan struct{}, 1)
	failed := make(chan error, 1)
	go func() { failed <- readEvents(events, changed) }()

	if err := p.refresh(ctx, update, true); err != nil {
		return false, err
	}
	tick := time.NewTicker(p.opts.Refresh())
	defer tick.Stop()
	var wait *time.Timer
	var fire <-chan time.Time
	defer func() {
		if wait != nil {
			wait.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case err := <-failed:
			if err == nil {
				err = errors.New("the daemon ended the event stream")
			}
			return true, fmt.Errorf("events: %w", err)
		case <-changed:
			if fire == nil {
				wait = time.NewTimer(debounce)
				fire = wait.C
			}
		case <-fire:
			fire = nil
			if err := p.refresh(ctx, update, false); err != nil {
				return true, err
			}
		case <-tick.C:
			if err := p.refresh(ctx, update, true); err != nil {
				return true, err
			}
		}
	}
}

// refresh lists the containers and sends what they provide, when it
// changed or always is set.
func (p *Provider) refresh(ctx context.Context, update func([]byte), always bool) error {
	var filters map[string][]string
	if !p.opts.ExposeAll {
		filters = map[string][]string{"label": {p.opts.Prefix() + ".enable=true"}}
	}
	cs, err := p.c.list(ctx, filters)
	if err != nil {
		return err
	}
	part, problems := translate(cs, p.opts)
	reported := make(map[string]bool, len(problems))
	for _, msg := range problems {
		if !p.problems[msg] {
			p.log.Warnf("provider %s: %s", p.name, msg)
		}
		reported[msg] = true
	}
	p.problems = reported
	if always || !bytes.Equal(part, p.last) {
		p.last = part
		update(part)
	}
	return nil
}

// readEvents reads the event stream, one JSON object a line, and signals
// changed for each event that may change what the containers provide: any
// of a container's but its exec_ ones, which health checks make. It returns
// when the stream ends.
func readEvents(r io.Reader, changed chan<- struct{}) error {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 4096), maxEventBytes)
	for s.Scan() {
		var e struct {
			Type   string `json:"Type"`
			Action string `json:"Action"`
		}
		if err := json.Unmarshal(s.Bytes(), &e); err != nil {
			return fmt.Errorf("an event that is not JSON: %w", err)
		}
		if e.Type != "" && e.Type != "container" || len(e.Action) >= 5 && e.Action[:5] == "exec_" {
			continue
		}
		select {
		case changed <- struct{}{}:
		default: // one is pending already
		}
	}
	return s.Err()
}

// client calls the daemon's API.
type client struct {
	hc      *http.Client
	base    string
	timeout time.Duration
}

func newClient(opts *config.DockerProvider) *client {
	network, address := opts.Dial()
	dialer := &net.Dialer{Timeout: opts.Timeout()}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, address)
		},
		MaxIdleConns:           2,
		IdleConnTimeout:        30 * time.Second,
		ResponseHeaderTimeout:  opts.Timeout(),
		MaxResponseHeaderBytes: 64 << 10,
		DisableCompression:     true,
	}
	host := "docker"
	if network == "tcp" {
		host = address
	}
	return &client{hc: &http.Client{Transport: tr}, base: "http://" + host, timeout: opts.Timeout()}
}

func (c *client) get(ctx context.Context, path string, filters map[string][]string) (*http.Response, error) {
	u := c.base + path
	if len(filters) > 0 {
		f, _ := json.Marshal(filters)
		u += "?filters=" + url.QueryEscape(string(f))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s: %s", path, resp.Status, bytes.TrimSpace(msg))
	}
	return resp, nil
}

// list returns the running containers filters keep, within the request
// timeout.
func (c *client) list(ctx context.Context, filters map[string][]string) ([]container, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, err := c.get(ctx, "/containers/json", filters)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxListBytes+1))
	if err != nil {
		return nil, fmt.Errorf("GET /containers/json: %w", err)
	}
	if len(data) > maxListBytes {
		return nil, fmt.Errorf("GET /containers/json: the list is larger than %d bytes", maxListBytes)
	}
	var cs []container
	if err := json.Unmarshal(data, &cs); err != nil {
		return nil, fmt.Errorf("GET /containers/json: %w", err)
	}
	return cs, nil
}

// events opens the stream of containers' events, which lasts until ctx
// ends or the daemon ends it.
func (c *client) events(ctx context.Context) (io.ReadCloser, error) {
	resp, err := c.get(ctx, "/events", map[string][]string{"type": {"container"}})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}
