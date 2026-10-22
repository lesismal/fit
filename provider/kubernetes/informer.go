package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sync"
	"time"
)

// Timings, variables for tests to shorten.
var (
	// minBackoff and maxBackoff bound the wait before listing a kind
	// again after a failure; a kind not installed is looked for again
	// every maxBackoff.
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
)

// kind is a kind of object the provider watches.
type kind struct {
	name string // the resource's plural, for messages
	path string // its path on the API server, across namespaces
	// older are paths of the kind's other versions, tried in turn when the
	// API server does not serve path.
	older []string
	// query narrows what is listed and watched, as a field selector.
	query url.Values
	// decode decodes an object, returning its store key and resource
	// version.
	decode func(data []byte) (obj any, m *meta, err error)
}

func kindOf[T any](name, path string, metaOf func(*T) *meta) kind {
	return kind{name: name, path: path, decode: func(data []byte) (any, *meta, error) {
		obj := new(T)
		if err := json.Unmarshal(data, obj); err != nil {
			return nil, nil, err
		}
		return obj, metaOf(obj), nil
	}}
}

func (k kind) or(older ...string) kind {
	k.older = older
	return k
}

func (k kind) where(selector string) kind {
	k.query = url.Values{"fieldSelector": {selector}}
	return k
}

const gatewayAPI = "/apis/gateway.networking.k8s.io/"

// The kinds the provider watches. TLSRoutes, TCPRoutes and UDPRoutes are
// in the Gateway API's experimental channel, at v1alpha2 so far.
var (
	kindNamespaces = kindOf("namespaces", "/api/v1/namespaces", func(o *Namespace) *meta { return &o.Metadata })
	kindClasses    = kindOf("gatewayclasses", gatewayAPI+"v1/gatewayclasses", func(o *GatewayClass) *meta { return &o.Metadata })
	kindGateways   = kindOf("gateways", gatewayAPI+"v1/gateways", func(o *Gateway) *meta { return &o.Metadata })
	kindRoutes     = kindOf("httproutes", gatewayAPI+"v1/httproutes", func(o *HTTPRoute) *meta { return &o.Metadata })
	kindGRPCRoutes = kindOf("grpcroutes", gatewayAPI+"v1/grpcroutes", func(o *GRPCRoute) *meta { return &o.Metadata })
	kindTLSRoutes  = kindOf("tlsroutes", gatewayAPI+"v1/tlsroutes", func(o *L4Route) *meta { return &o.Metadata }).
			or(gatewayAPI+"v1alpha3/tlsroutes", gatewayAPI+"v1alpha2/tlsroutes")
	kindTCPRoutes = kindOf("tcproutes", gatewayAPI+"v1alpha2/tcproutes", func(o *L4Route) *meta { return &o.Metadata })
	kindUDPRoutes = kindOf("udproutes", gatewayAPI+"v1alpha2/udproutes", func(o *L4Route) *meta { return &o.Metadata })
	kindGrants    = kindOf("referencegrants", gatewayAPI+"v1beta1/referencegrants", func(o *ReferenceGrant) *meta { return &o.Metadata }).
			or(gatewayAPI + "v1/referencegrants")
	kindServices = kindOf("services", "/api/v1/services", func(o *Service) *meta { return &o.Metadata })
	kindSlices   = kindOf("endpointslices", "/apis/discovery.k8s.io/v1/endpointslices", func(o *EndpointSlice) *meta { return &o.Metadata })
	// Only the Secrets of TLS certificates, which HTTPS listeners name.
	kindSecrets = kindOf("secrets", "/api/v1/secrets", func(o *Secret) *meta { return &o.Metadata }).
			where("type=kubernetes.io/tls")
)

// informer keeps the objects of one kind as the API server has them: it
// lists them, then watches them from the list's resource version, and lists
// again when the watch fails or is too old.
type informer struct {
	kind
	c       *client
	max     int
	changed chan<- struct{}
	logf    func(format string, args ...any)

	mu     sync.Mutex
	objs   map[string]any
	synced bool // a list has been taken, or the kind is not installed
	// at is the path of the version the API server serves: path, or one of
	// older.
	at string

	// What was reported, so that it is reported once: the kind missing,
	// and objects left out past max.
	missing, full bool
}

func newInformer(k kind, c *client, max int, changed chan<- struct{}, logf func(string, ...any)) *informer {
	return &informer{kind: k, c: c, max: max, changed: changed, logf: logf, objs: map[string]any{}, at: k.path}
}

// served returns the path of the kind's version the API server serves.
func (in *informer) served() string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.at
}

func (in *informer) signal() {
	select {
	case in.changed <- struct{}{}:
	default: // one is pending already
	}
}

// isSynced reports whether the kind has been listed.
func (in *informer) isSynced() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.synced
}

// values returns the objects held, of type *T.
func values[T any](in *informer) []*T {
	in.mu.Lock()
	defer in.mu.Unlock()
	out := make([]*T, 0, len(in.objs))
	for _, o := range in.objs {
		out = append(out, o.(*T))
	}
	return out
}

// run keeps the store up to date until ctx ends.
func (in *informer) run(ctx context.Context) {
	backoff := minBackoff
	wait := func(d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			return true
		}
	}
	for ctx.Err() == nil {
		rv, err := in.list(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, errNotFound):
			if !in.missing {
				in.missing = true
				in.logf("%s are not served by the API server (is the Gateway API installed?); looking again every %s", in.name, maxBackoff)
			}
			in.replace(nil, false)
			if !wait(maxBackoff) {
				return
			}
			continue
		case err != nil:
			in.logf("listing %s: %v; trying again in %s", in.name, err, backoff)
			if !wait(backoff) {
				return
			}
			backoff = min(2*backoff, maxBackoff)
			continue
		}
		if in.missing {
			in.missing = false
			in.logf("%s are served now", in.name)
		}
		backoff = minBackoff
		for {
			rv, err = in.watch(ctx, rv)
			if ctx.Err() != nil {
				return
			}
			if err == nil {
				continue // the watch ended in time: watch again from rv
			}
			if !errors.Is(err, errGone) {
				in.logf("watching %s: %v; listing them again in %s", in.name, err, backoff)
				if !wait(backoff) {
					return
				}
				backoff = min(2*backoff, maxBackoff)
			}
			break
		}
	}
}

// list lists the kind, at the first of its versions the API server
// serves, and replaces the store with it.
func (in *informer) list(ctx context.Context) (string, error) {
	var items []json.RawMessage
	var rv string
	var truncated bool
	var err error
	for _, path := range append([]string{in.path}, in.older...) {
		items, rv, truncated, err = in.c.list(ctx, path, in.query, in.max)
		if !errors.Is(err, errNotFound) {
			in.mu.Lock()
			in.at = path
			in.mu.Unlock()
			break
		}
	}
	if err != nil {
		return "", err
	}
	objs := make(map[string]any, len(items))
	for _, data := range items {
		obj, m, err := in.decode(data)
		if err != nil {
			return "", fmt.Errorf("%s: %w", in.name, err)
		}
		objs[m.key()] = obj
	}
	in.replace(objs, truncated)
	return rv, nil
}

func (in *informer) replace(objs map[string]any, truncated bool) {
	if objs == nil {
		objs = map[string]any{}
	}
	in.mu.Lock()
	in.objs, in.synced = objs, true
	in.mu.Unlock()
	in.reportFull(truncated)
	in.signal()
}

func (in *informer) reportFull(full bool) {
	if full && !in.full {
		in.logf("more than max_objects, %d, %s: the rest are left out", in.max, in.name)
	}
	in.full = full
}

// watch applies the watch's events from rv until it ends, and returns the
// resource version it reached. A nil error means the server ended the
// watch in time; errGone, that rv is too old to watch from.
func (in *informer) watch(ctx context.Context, rv string) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	body, err := in.c.watch(ctx, in.served(), in.query, rv)
	if err != nil {
		return rv, err
	}
	defer body.Close()
	// A watch that stops sending, bookmarks included, past its time is
	// given up, so that a half-open connection does not stall the kind.
	idle := time.AfterFunc(2*watchSeconds*time.Second, cancel)
	defer idle.Stop()
	dec := json.NewDecoder(body)
	for {
		var e struct {
			Type   string          `json:"type"`
			Object json.RawMessage `json:"object"`
		}
		if err := dec.Decode(&e); err != nil {
			if errors.Is(err, io.EOF) {
				return rv, nil
			}
			return rv, err
		}
		idle.Reset(2 * watchSeconds * time.Second)
		switch e.Type {
		case "ERROR":
			var status struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			}
			_ = json.Unmarshal(e.Object, &status)
			if status.Code == 410 {
				return rv, errGone
			}
			return rv, fmt.Errorf("the watch failed: %d %s", status.Code, status.Message)
		case "BOOKMARK":
			var o struct {
				Metadata meta `json:"metadata"`
			}
			if err := json.Unmarshal(e.Object, &o); err == nil && o.Metadata.ResourceVersion != "" {
				rv = o.Metadata.ResourceVersion
			}
		case "ADDED", "MODIFIED", "DELETED":
			obj, m, err := in.decode(e.Object)
			if err != nil {
				return rv, fmt.Errorf("an event of %s: %w", in.name, err)
			}
			if m.ResourceVersion != "" {
				rv = m.ResourceVersion
			}
			in.mu.Lock()
			key := m.key()
			_, had := in.objs[key]
			full := false
			switch {
			case e.Type == "DELETED":
				delete(in.objs, key)
			case had || len(in.objs) < in.max:
				in.objs[key] = obj
			default:
				full = true
			}
			in.mu.Unlock()
			if full {
				in.reportFull(true)
				continue
			}
			in.signal()
		}
	}
}
