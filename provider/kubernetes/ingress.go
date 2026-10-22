package kubernetes

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
)

// annotationPrefix is the prefix of the annotations of Ingresses fit reads.
const annotationPrefix = "fit.lesismal.github.io/"

// ingressAnnotations is what an Ingress's annotations ask of its routes.
type ingressAnnotations struct {
	sslRedirect bool    // ssl-redirect: HTTP requests for its TLS hosts are redirected to HTTPS
	rewrite     *string // rewrite-target: the path its paths' matches are replaced with
	h2c         bool    // backend-protocol: H2C or GRPC
	rate        float64 // limit-rps: requests a second from each client's address; 0 for no limit
	bodyBytes   int64   // proxy-body-size: the most bytes of a request body; 0 for no limit
	allow       []string
	denyAll     bool   // allow-source-range does not parse: every request is refused
	timeout     string // request-timeout: the proxy's timeout
}

// ingresses translates fit's Ingresses, with ingress set: those naming an
// IngressClass whose controller is fit's, by ingressClassName or the
// kubernetes.io/ingress.class annotation, and those naming none when such a
// class is the default. Ingresses have no status of fit's.
func (t *translator) ingresses() {
	if t.opts.Ingress == nil {
		return
	}
	classes, anyDefault := map[string]bool{}, false
	for _, c := range t.s.ingressClasses {
		if c.Spec.Controller == t.opts.Controller() {
			classes[c.Metadata.Name] = true
			anyDefault = anyDefault || c.Metadata.Annotations["ingressclass.kubernetes.io/is-default-class"] == "true"
		}
	}
	for _, ing := range byCreation(t.s.ingresses, func(i *Ingress) *meta { return &i.Metadata }) {
		class := deref(ing.Spec.IngressClassName, ing.Metadata.Annotations["kubernetes.io/ingress.class"])
		if t.watched(ing.Metadata.Namespace) && (class == "" && anyDefault || classes[class]) {
			t.ingress(ing)
		}
	}
	t.cur = nil
}

// ingress translates ing: each path of each rule, and its default backend,
// is a route of fit's on the listener of the HTTP port and, for the hosts of
// its tls, on that of the HTTPS port, with the certificates of its Secrets.
func (t *translator) ingress(ing *Ingress) {
	ns, key := ing.Metadata.Namespace, ing.Metadata.key()
	where := "ingress " + key
	// For upstream: an Ingress's backends are in its namespace.
	t.cur = &routeState{kind: "Ingress", m: &ing.Metadata}
	a := t.annotations(where, ing.Metadata.Annotations)
	ports := t.opts.Ingress

	httpL, other := t.portListener(ports.HTTP(), familyHTTP)
	if httpL == nil {
		t.problem("%s: port %d serves %s already: its rules are not served over HTTP", where, ports.HTTP(), other)
	}
	var certs []outCert
	var tlsHosts []string
	tlsAny := false
	for i, tl := range ing.Spec.TLS {
		switch {
		case tl.SecretName == "":
			t.problem("%s: tls %d: no secretName", where, i)
			continue
		case t.certDir == "":
			t.problem("%s: tls %d: there is no directory to write certificates to", where, i)
			continue
		}
		c, err := t.secretCert(ns, tl.SecretName)
		if err != nil {
			t.problem("%s: tls %d: %v", where, i, err)
			continue
		}
		certs = append(certs, c)
		if len(tl.Hosts) == 0 {
			tlsAny = true
		}
		tlsHosts = append(tlsHosts, tl.Hosts...)
	}
	var httpsL *fitListener
	if len(certs) > 0 {
		if httpsL, other = t.portListener(ports.HTTPS(), familyTLS); httpsL == nil {
			t.problem("%s: port %d serves %s already: its rules are not served over HTTPS", where, ports.HTTPS(), other)
		} else {
			httpsL.addCerts(certs)
		}
	}
	// secure reports whether host, "" for any, is served over HTTPS.
	secure := func(host string) bool {
		return httpsL != nil && (tlsAny || host != "" && slices.ContainsFunc(tlsHosts, func(h string) bool { return hostMatches(h, host) }))
	}

	for i, rule := range ing.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		var hosts []string
		hostClass := 0
		if rule.Host != "" {
			hosts, hostClass = []string{rule.Host}, 2
			if strings.HasPrefix(rule.Host, "*") {
				hostClass = 1
			}
		}
		for j, p := range rule.HTTP.Paths {
			w := fmt.Sprintf("%s: rule %d: path %d", where, i, j)
			path := cmp.Or(p.Path, "/")
			if !strings.HasPrefix(path, "/") {
				t.problem("%s: path %q does not start with '/'", w, path)
				continue
			}
			om := &outMatch{Host: hosts}
			pathClass, pathType, prefix := 2, "PathPrefix", ""
			switch p.PathType {
			case "Exact":
				om.Path, pathClass, pathType = []string{path}, 3, "Exact"
			case "Prefix", "ImplementationSpecific":
				// fit's own reading of ImplementationSpecific is Prefix.
				if prefix = strings.TrimRight(path, "/"); prefix != "" {
					om.Path = []string{prefix, prefix + "/*"}
				}
			default:
				t.problem("%s: path type %q is not supported", w, p.PathType)
				continue
			}
			priority := hostClass*100_000_000 + pathClass*10_000_000 + min(len(path), 9999)*1000
			name := fmt.Sprintf("%s.%s.ing.r%d.p%d", ns, ing.Metadata.Name, i, j)
			t.ingressRoutes(w, name, om, priority, rule.Host, secure(rule.Host), httpL, httpsL, a, p.Backend, pathType, prefix)
		}
	}
	if b := ing.Spec.DefaultBackend; b != nil {
		name := fmt.Sprintf("%s.%s.ing.default", ns, ing.Metadata.Name)
		t.ingressRoutes(where+": defaultBackend", name, &outMatch{}, 0, "", secure(""), httpL, httpsL, a, *b, "PathPrefix", "")
	}
}

// ingressRoutes makes the routes of a path of an Ingress, or of its default
// backend, named name: the match om, of priority, for host ("" for any), on
// the listener of the HTTP port and, when secure, on that of the HTTPS
// port, unless a redirects its HTTP to HTTPS; with a source range, a route
// refusing the other clients beside it.
func (t *translator) ingressRoutes(where, name string, om *outMatch, priority int, host string, secure bool,
	httpL, httpsL *fitListener, a ingressAnnotations, b IngressBackend, pathType, prefix string) {
	var mws []json.RawMessage
	if a.rate > 0 {
		mw, _ := json.Marshal(map[string]any{"module": "rate_limit", "params": map[string]any{"rate": a.rate}})
		mws = append(mws, mw)
	}
	if a.bodyBytes > 0 {
		mw, _ := json.Marshal(map[string]any{"module": "body_limit", "params": map[string]any{"max_bytes": a.bodyBytes}})
		mws = append(mws, mw)
	}
	if a.rewrite != nil {
		pm := &HTTPPathModifier{Type: "ReplacePrefixMatch", ReplacePrefixMatch: a.rewrite}
		if pathType == "Exact" {
			pm = &HTTPPathModifier{Type: "ReplaceFullPath", ReplaceFullPath: a.rewrite}
		}
		if mw, err := rewrite(pm, pathType, prefix); err != nil {
			t.problem("%s: rewrite-target: %v", where, err)
		} else if mw != nil {
			mws = append(mws, mw)
		}
	}
	handler := t.ingressBackend(where, b, a)

	redirect := ""
	if a.sslRedirect && secure && httpL != nil {
		port := ""
		if p := t.opts.Ingress.HTTPS(); p != 443 {
			port = ":" + strconv.Itoa(p)
		}
		switch {
		case host != "" && !strings.HasPrefix(host, "*"):
			redirect = "https://" + host + port + "{uri}"
		case t.opts.Ingress.HTTP() == 80 && port == "":
			redirect = "https://{host}{uri}"
		default:
			t.problem("%s: ssl-redirect: a wildcard or any host is redirected only from port 80 to 443; it is served over HTTP", where)
		}
	}
	var listeners []string
	if httpL != nil && redirect == "" {
		listeners = append(listeners, httpL.name)
	}
	if secure {
		listeners = append(listeners, httpsL.name)
	}
	if len(listeners) > 0 {
		t.ingressRoute(name, listeners, om, priority, mws, handler, a)
	}
	if redirect != "" {
		mw, _ := json.Marshal(map[string]any{"module": "redirect", "params": map[string]any{"status": 308, "to": redirect}})
		t.ingressRoute(name+".redirect", []string{httpL.name}, om, priority, []json.RawMessage{mw}, respond(404), a)
	}
}

// ingressRoute adds the route name, and the one refusing the clients a's
// source ranges leave out.
func (t *translator) ingressRoute(name string, listeners []string, om *outMatch, priority int, mws []json.RawMessage,
	handler json.RawMessage, a ingressAnnotations) {
	match := func(m *outMatch) *outMatch {
		if m.Host == nil && m.Path == nil && m.RemoteIP == nil {
			return nil
		}
		return m
	}
	switch {
	case a.denyAll:
		t.routes = append(t.routes, &outRoute{Name: name, Listeners: listeners, Priority: priority, Match: match(om), Handler: respond(403)})
	case len(a.allow) > 0:
		allowed := *om
		allowed.RemoteIP = a.allow
		t.routes = append(t.routes,
			&outRoute{Name: name, Listeners: listeners, Priority: priority + 1, Match: &allowed, Middleware: mws, Handler: handler},
			&outRoute{Name: name + ".deny", Listeners: listeners, Priority: priority, Match: match(om), Handler: respond(403)})
	default:
		t.routes = append(t.routes, &outRoute{Name: name, Listeners: listeners, Priority: priority, Match: match(om), Middleware: mws, Handler: handler})
	}
}

// ingressBackend returns the handler sending to backend b: the proxy, to
// the upstream of a Service's port, or 503 when it cannot be.
func (t *translator) ingressBackend(where string, b IngressBackend, a ingressAnnotations) json.RawMessage {
	if b.Service == nil {
		t.problem("%s: only a Service is supported as a backend; it answers 503", where)
		return respond(503)
	}
	port := b.Service.Port.Number
	if port == 0 && b.Service.Port.Name != "" {
		if svc := t.s.service[t.cur.m.Namespace+"/"+b.Service.Name]; svc != nil {
			for _, sp := range svc.Spec.Ports {
				if sp.Name == b.Service.Port.Name {
					port = sp.Port
				}
			}
		}
		if port == 0 {
			t.problem("%s: service %s/%s has no port named %q; it answers 503", where, t.cur.m.Namespace, b.Service.Name, b.Service.Port.Name)
			return respond(503)
		}
	}
	name, ok, err := t.upstream(BackendObjectRef{Name: b.Service.Name, Port: &port}, "TCP", a.h2c)
	switch {
	case err != nil:
		t.problem("%s: %v; it answers 503", where, err)
		return respond(503)
	case !ok:
		return respond(503)
	}
	params := map[string]any{"upstream": name}
	if a.timeout != "" {
		params["timeout"] = a.timeout
	}
	h, _ := json.Marshal(map[string]any{"module": "proxy", "params": params})
	return h
}

// annotations reads the annotations of an Ingress under annotationPrefix,
// saying which it leaves out and why.
func (t *translator) annotations(where string, ann map[string]string) ingressAnnotations {
	var a ingressAnnotations
	for _, k := range sortedKeys(ann) {
		name, ok := strings.CutPrefix(k, annotationPrefix)
		if !ok {
			continue
		}
		v := strings.TrimSpace(ann[k])
		bad := func(format string, args ...any) {
			t.problem("%s: annotation %s: %s; it is left out", where, k, fmt.Sprintf(format, args...))
		}
		switch name {
		case "ssl-redirect":
			b, err := strconv.ParseBool(v)
			if err != nil {
				bad("%q is not true or false", v)
				continue
			}
			a.sslRedirect = b
		case "rewrite-target":
			if !strings.HasPrefix(v, "/") {
				bad("%q is not a path", v)
				continue
			}
			a.rewrite = &v
		case "backend-protocol":
			switch strings.ToUpper(v) {
			case "HTTP":
			case "H2C", "GRPC":
				a.h2c = true
			default:
				bad("%q is not HTTP, H2C or GRPC", v)
			}
		case "limit-rps":
			r, err := strconv.ParseFloat(v, 64)
			if err != nil || r < 0.001 {
				bad("%q is not a rate of at least 0.001", v)
				continue
			}
			a.rate = r
		case "proxy-body-size":
			n, err := parseSize(v)
			if err != nil {
				bad("%v", err)
				continue
			}
			a.bodyBytes = n
		case "allow-source-range":
			for r := range strings.SplitSeq(v, ",") {
				r = strings.TrimSpace(r)
				if r == "" {
					continue
				}
				if _, err := netip.ParsePrefix(r); err != nil {
					if _, err := netip.ParseAddr(r); err != nil {
						// A range that does not parse must not open the
						// Ingress to everyone: it is closed instead.
						t.problem("%s: annotation %s: %q is not an address or CIDR range; every request is refused", where, k, r)
						a.denyAll = true
						continue
					}
				}
				a.allow = append(a.allow, r)
			}
		case "request-timeout":
			if d, err := time.ParseDuration(v); err != nil || d <= 0 {
				bad("%q is not a duration, as 30s", v)
				continue
			}
			a.timeout = v
		default:
			t.problem("%s: annotation %s is not one fit knows", where, k)
		}
	}
	return a
}

// parseSize parses a size in bytes, as nginx writes them: a number, maybe
// followed by k or m (case aside), for KiB and MiB; 0 for no limit.
func parseSize(size string) (int64, error) {
	v, mult := size, int64(1)
	switch {
	case strings.HasSuffix(strings.ToLower(v), "k"):
		mult, v = 1<<10, v[:len(v)-1]
	case strings.HasSuffix(strings.ToLower(v), "m"):
		mult, v = 1<<20, v[:len(v)-1]
	case strings.HasSuffix(strings.ToLower(v), "g"):
		mult, v = 1<<30, v[:len(v)-1]
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 || n > 1<<40/mult {
		return 0, fmt.Errorf("%q is not a size, as 8m", size)
	}
	return n * mult, nil
}
