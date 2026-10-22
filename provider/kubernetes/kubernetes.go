// Package kubernetes is the Kubernetes provider: it serves the Gateway
// API's Gateways and HTTPRoutes of fit's GatewayClasses.
//
// It talks to the API server with a client of its own on net/http, listing
// and watching each kind it reads; see docs/decisions/011-kubernetes-client.md.
// Each HTTP listener of a Gateway whose GatewayClass names fit's
// controllerName is a listener of fit's on that port (one per port, shared
// by Gateways), each HTTPRoute attached to one is a route of fit's for each
// of its rules' matches, and each Service port they send to is an upstream,
// of its ready endpoints or its cluster IP. Which listener takes which
// route follows the Gateway API: parentRefs' sections and ports, listeners'
// allowedRoutes (Same, All and Selector namespaces, and kinds) and host
// names; a backend in another namespace needs a ReferenceGrant. Routes keep
// the API's precedence as their priority. What the API allows and fit does
// not serve yet (listeners other than HTTP, header and query matches by
// regular expression, filters on backends) is left out and reported.
//
// It runs in the control plane, on goroutines of its own.
package kubernetes

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
)

// Timings, variables for tests to shorten.
var (
	// debounce is how long the provider waits after a change for more,
	// before translating what it holds.
	debounce = 200 * time.Millisecond
	// resync is how often the part is sent whether or not it changed, so
	// that one refused because of the rest of the config is tried again.
	resync = 30 * time.Second
)

// Provider is a Kubernetes provider.
type Provider struct {
	name string
	opts *config.KubernetesProvider
	logf func(format string, args ...any)

	last     []byte
	problems map[string]bool
}

// New returns a Kubernetes provider named name, with the settings opts.
// logf, which may be nil, hears what the provider reports.
func New(name string, opts *config.KubernetesProvider, logf func(format string, args ...any)) *Provider {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Provider{name: name, opts: opts, logf: func(format string, args ...any) {
		logf("provider "+name+": "+format, args...)
	}}
}

// Name returns the provider's name.
func (p *Provider) Name() string { return p.name }

// Run follows the cluster until ctx ends, calling update with what it
// provides once every kind has been listed, after each change, and every
// 30s whether or not it changed. Until the cluster can be reached, and
// while it cannot, it sends nothing new, so the last part stays.
func (p *Provider) Run(ctx context.Context, update func([]byte)) {
	var rc *restConfig
	for {
		var err error
		if rc, err = loadConfig(p.opts); err == nil {
			break
		}
		p.logf("%v; trying again in %s", err, maxBackoff)
		t := time.NewTimer(maxBackoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
	p.logf("watching the cluster at %s, by %s", rc.server, rc.where)
	c := newClient(rc, p.opts.Timeout())
	changed := make(chan struct{}, 1)
	kinds := []kind{kindNamespaces, kindClasses, kindGateways, kindRoutes, kindGrants, kindServices, kindSlices}
	informers := make([]*informer, len(kinds))
	var wg sync.WaitGroup
	for i, k := range kinds {
		informers[i] = newInformer(k, c, p.opts.Objects(), changed, p.logf)
		wg.Add(1)
		go func() {
			defer wg.Done()
			informers[i].run(ctx)
		}()
	}
	defer wg.Wait()
	defer c.hc.CloseIdleConnections()

	tick := time.NewTicker(resync)
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
			return
		case <-changed:
			if fire == nil {
				wait = time.NewTimer(debounce)
				fire = wait.C
			}
		case <-fire:
			fire = nil
			p.send(informers, update, false)
		case <-tick.C:
			p.send(informers, update, true)
		}
	}
}

// send translates what the informers hold and sends it, when it changed or
// always is set, once every kind has been listed.
func (p *Provider) send(in []*informer, update func([]byte), always bool) {
	for _, i := range in {
		if !i.isSynced() {
			return
		}
	}
	s := newSnapshot(values[Namespace](in[0]), values[GatewayClass](in[1]), values[Gateway](in[2]), values[HTTPRoute](in[3]),
		values[ReferenceGrant](in[4]), values[Service](in[5]), values[EndpointSlice](in[6]))
	part, problems := translate(p.name, p.opts, s)
	reported := make(map[string]bool, len(problems))
	for _, msg := range problems {
		if !p.problems[msg] {
			p.logf("%s", msg)
		}
		reported[msg] = true
	}
	p.problems = reported
	if always || !bytes.Equal(part, p.last) {
		p.last = part
		update(part)
	}
}
