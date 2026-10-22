// Package kubernetes is the Kubernetes provider: it serves the Gateway
// API's Gateways of fit's GatewayClasses and their routes, and writes their
// status back.
//
// It talks to the API server with a client of its own on net/http, listing
// and watching each kind it reads; see docs/decisions/011-kubernetes-client.md.
// The listeners of a Gateway whose GatewayClass names fit's controllerName
// are listeners of fit's, one for each port (shared by Gateways) and family:
// HTTP; HTTPS, with the certificates of the Secrets it names, and TLS passed
// through, told apart by server name on one port; TCP; UDP. Each HTTPRoute
// and GRPCRoute attached to one is a route of fit's for each of its rules'
// matches, each TLSRoute, TCPRoute and UDPRoute a service, and each Service
// port they send to an upstream, of its ready endpoints or its cluster IP.
// Which listener takes which route follows the Gateway API: parentRefs'
// sections and ports, listeners' allowedRoutes (Same, All and Selector
// namespaces, and kinds) and host names; a backend or certificate in
// another namespace needs a ReferenceGrant. Routes keep the API's
// precedence as their priority. What the API allows and fit does not serve
// yet (TLS terminated on a TLS listener, header and query matches by
// regular expression, filters on backends) is left out, reported, and said
// in the status: each GatewayClass, Gateway and listener, and each route on
// each of fit's Gateways, has its conditions written back.
//
// It runs in the control plane, on goroutines of its own.
package kubernetes

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
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
	// certDir is where HTTPS listeners' certificates are written; "" when
	// there is nowhere.
	certDir string
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

// watched returns the kinds a provider of opts watches: Ingresses and
// IngressClasses only with ingress.
func watched(opts *config.KubernetesProvider) []kind {
	kinds := []kind{kindNamespaces, kindClasses, kindGateways, kindRoutes, kindGRPCRoutes, kindTLSRoutes, kindTCPRoutes,
		kindUDPRoutes, kindGrants, kindServices, kindSlices, kindSecrets}
	if opts.Ingress != nil {
		kinds = append(kinds, kindIngresses, kindIngressClasses)
	}
	return kinds
}

// Run follows the cluster until ctx ends, calling update with what it
// provides once every kind has been listed, after each change, and every
// 30s whether or not it changed. Until the cluster can be reached, and
// while it cannot, it sends nothing new, so the last part stays. After
// each part it writes the status of the objects whose status changed.
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
	if dir := p.opts.CertificatesDir; dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			p.logf("certificates_dir: %v; HTTPS listeners are not served", err)
		} else {
			p.certDir = dir
		}
	} else if dir, err := os.MkdirTemp("", "fit-"+p.name+"-"); err != nil {
		p.logf("no directory for the certificates of HTTPS listeners: %v; they are not served", err)
	} else {
		p.certDir = dir
		defer os.RemoveAll(dir)
	}
	c := newClient(rc, p.opts.Timeout())
	changed := make(chan struct{}, 1)
	informers := map[string]*informer{}
	var wg sync.WaitGroup
	for _, k := range watched(p.opts) {
		in := newInformer(k, c, p.opts.Objects(), changed, p.logf)
		informers[k.name] = in
		wg.Add(1)
		go func() {
			defer wg.Done()
			in.run(ctx)
		}()
	}
	sw := &statusWriter{c: c, informers: informers, logf: p.logf, wake: make(chan struct{}, 1), failing: map[string]bool{}}
	wg.Add(1)
	go func() {
		defer wg.Done()
		sw.run(ctx)
	}()
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
			p.send(informers, update, sw, false)
		case <-tick.C:
			p.send(informers, update, sw, true)
		}
	}
}

// send translates what the informers hold and sends it, when it changed or
// always is set, once every kind has been listed; then it hands the
// statuses to write to sw.
func (p *Provider) send(in map[string]*informer, update func([]byte), sw *statusWriter, always bool) {
	for _, i := range in {
		if !i.isSynced() {
			return
		}
	}
	s := newSnapshot(objects{
		namespaces: values[Namespace](in[kindNamespaces.name]),
		classes:    values[GatewayClass](in[kindClasses.name]),
		gateways:   values[Gateway](in[kindGateways.name]),
		routes:     values[HTTPRoute](in[kindRoutes.name]),
		grpcRoutes: values[GRPCRoute](in[kindGRPCRoutes.name]),
		tlsRoutes:  values[L4Route](in[kindTLSRoutes.name]),
		tcpRoutes:  values[L4Route](in[kindTCPRoutes.name]),
		udpRoutes:  values[L4Route](in[kindUDPRoutes.name]),
		grants:     values[ReferenceGrant](in[kindGrants.name]),
		services:   values[Service](in[kindServices.name]),
		slices:     values[EndpointSlice](in[kindSlices.name]),
		secrets:    values[Secret](in[kindSecrets.name]),
	})
	if p.opts.Ingress != nil {
		s.ingresses, s.ingressClasses = values[Ingress](in[kindIngresses.name]), values[IngressClass](in[kindIngressClasses.name])
	}
	t := newTranslator(p.name, p.opts, s, p.certDir)
	part := t.run()
	reported := make(map[string]bool, len(t.problems))
	for _, msg := range t.problems {
		if !p.problems[msg] {
			p.logf("%s", msg)
		}
		reported[msg] = true
	}
	p.problems = reported
	if err := writeCerts(t.certs); err != nil {
		p.logf("writing certificates: %v; the change is not sent", err)
		return
	}
	if always || !bytes.Equal(part, p.last) {
		p.last = part
		update(part)
	}
	removeCerts(p.certDir, t.certs)
	sw.set(t.statuses(time.Now()))
}

// certFile is the name of a certificate file the provider writes:
// <namespace>.<secret>.<hash>.pem.
var certFile = regexp.MustCompile(`^[a-z0-9-]+\.[a-z0-9.-]+\.[0-9a-f]{12}\.pem$`)

// writeCerts writes the certificate files certs, by path, that are not
// there yet: a file's name holds a hash of its contents, so one there is
// the same. Each is written whole, then renamed into place.
func writeCerts(certs map[string][]byte) error {
	for _, path := range sortedKeys(certs) {
		if _, err := os.Stat(path); err == nil {
			continue
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, certs[path], 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	return nil
}

// removeCerts removes the certificate files in dir that certs no longer
// holds.
func removeCerts(dir string, certs map[string][]byte) {
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if certFile.MatchString(e.Name()) && certs[path] == nil {
			os.Remove(path)
		}
	}
}

// statusWriter writes the statuses the provider makes, on a goroutine of
// its own, so that a slow API server holds up no part: always the last set
// it was given. A write refused because the object changed since is left,
// since the change comes through its watch and makes another set.
type statusWriter struct {
	c         *client
	informers map[string]*informer // by kind
	logf      func(format string, args ...any)
	wake      chan struct{}
	// failing holds the kinds whose last write failed, and was reported;
	// run's alone.
	failing map[string]bool

	mu   sync.Mutex
	next []statusUpdate
}

func (w *statusWriter) set(us []statusUpdate) {
	w.mu.Lock()
	w.next = us
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *statusWriter) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		}
		w.mu.Lock()
		us := w.next
		w.next = nil
		w.mu.Unlock()
		for _, u := range us {
			if ctx.Err() != nil {
				return
			}
			w.write(ctx, u)
		}
	}
}

func (w *statusWriter) write(ctx context.Context, u statusUpdate) {
	path := objectPath(w.informers[u.kind].served(), u.ns, u.name)
	err := w.c.patchStatus(ctx, path, u.patch)
	switch {
	case ctx.Err() != nil:
	case err == nil, errors.Is(err, errConflict), errors.Is(err, errNotFound):
		if w.failing[u.kind] {
			delete(w.failing, u.kind)
			w.logf("the status of %s is written again", u.kind)
		}
	case !w.failing[u.kind]:
		w.failing[u.kind] = true
		w.logf("writing the status of %s: %v; trying again with each change and every %s", u.kind, err, resync)
	}
}
