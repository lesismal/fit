package kubernetes

import (
	"fmt"
	"math"
	"strings"
)

// l4Route translates r, a TLSRoute, TCPRoute or UDPRoute as kind says: a
// service of fit's on the listeners r is attached to, to its backends, for
// a TLSRoute the connections whose ClientHello names its host names.
func (t *translator) l4Route(kind string, r *L4Route) {
	t.begin(kind, &r.Metadata, r.Spec.ParentRefs, &r.Status)
	var hostnames []string
	if kind == "TLSRoute" {
		hostnames = r.Spec.Hostnames
	}
	groups := t.attach(hostnames)
	// A route taken only by listeners fit does not serve makes no routes
	// of fit's, but its backends are still resolved, for its status.
	if len(groups) == 0 && !t.cur.accepted() {
		return
	}
	where := fmt.Sprintf("%s %s", strings.ToLower(kind), r.Metadata.key())
	if len(r.Spec.Rules) == 0 {
		t.badRef(where, badRef("BackendNotFound", "it has no rules, and so no backends"))
		return
	}
	if len(r.Spec.Rules) > 1 {
		t.leftOut("%s: only its first rule is served; the others are left out", where)
	}
	proto, tag := "TCP", strings.ToLower(strings.TrimSuffix(kind, "Route"))
	if kind == "UDPRoute" {
		proto = "UDP"
	}
	base := fmt.Sprintf("%s.%s.%s", r.Metadata.Namespace, r.Metadata.Name, tag)
	upstream := t.l4Upstream(where+": rule 0", base, r.Spec.Rules[0].BackendRefs, proto)
	if upstream == "" {
		return
	}
	for gi, at := range groups {
		name := base
		if len(groups) > 1 {
			name += fmt.Sprintf(".g%d", gi)
		}
		t.services = append(t.services, &outService{Name: name, Listeners: at.listeners, Upstream: upstream, ServerNames: at.hosts})
		for _, l := range at.listeners {
			t.listeners[l].services++
		}
	}
}

// l4Upstream returns the upstream of the backends refs, of the rule at
// where, whose ports are of protocol proto: one Service port's own, or
// one named name of several Services' endpoints, each weighted by its
// Service's weight shared among them; "" when none has endpoints ready.
func (t *translator) l4Upstream(where, name string, refs []BackendRef, proto string) string {
	type backend struct {
		up     *outUpstream
		weight int
	}
	var bs []backend
	for k, ref := range refs {
		w := deref(ref.Weight, 1)
		if w == 0 {
			continue
		}
		up, ok, err := t.upstream(ref.BackendObjectRef, proto, false)
		switch {
		case err != nil:
			t.badRef(fmt.Sprintf("%s: backend %d", where, k), err)
		case ok:
			bs = append(bs, backend{t.upstreams[up], w})
		}
	}
	switch len(bs) {
	case 0:
		t.problem("%s: no backend has endpoints ready; its connections are closed", where)
		return ""
	case 1:
		return bs[0].up.Name
	}
	share := 0.0
	for _, b := range bs {
		share = max(share, float64(b.weight)/float64(len(b.up.Addresses)))
	}
	u := &outUpstream{Name: name}
	at := map[string]int{}
	for _, b := range bs {
		w := max(1, int(math.Round(1000*float64(b.weight)/float64(len(b.up.Addresses))/share)))
		for _, a := range b.up.Addresses {
			if i, ok := at[a]; ok {
				u.Weights[i] = min(1000, u.Weights[i]+w)
				continue
			}
			at[a] = len(u.Addresses)
			u.Addresses = append(u.Addresses, a)
			u.Weights = append(u.Weights, w)
		}
	}
	t.upstreams[name] = u
	return name
}
