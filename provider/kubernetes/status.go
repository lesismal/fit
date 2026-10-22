package kubernetes

import (
	"bytes"
	"encoding/json"
	"net"
	"strings"
	"time"
)

// maxParents is how many parents a route's status holds, as the Gateway
// API bounds it.
const maxParents = 32

// statusUpdate is a status to write: a JSON merge patch of the status of
// the object of a kind, by its plural, and its namespace and name.
type statusUpdate struct {
	kind, ns, name string
	patch          []byte
}

// The plural of each kind of route, which names its informer.
var routePlurals = map[string]string{
	"HTTPRoute": "httproutes",
	"GRPCRoute": "grpcroutes",
	"TLSRoute":  "tlsroutes",
	"TCPRoute":  "tcproutes",
	"UDPRoute":  "udproutes",
}

// statuses returns, as patches, the statuses fit's GatewayClasses, Gateways
// and the routes in the namespaces watched should have, after run, where
// they differ from those they have: whether each is accepted and
// programmed, each of a Gateway's listeners, and each route's on each of
// fit's Gateways. A condition that changes is said to have changed at now;
// one that does not keeps its time. Each patch names the resource version
// it was made from, so that it is refused if the object changed since.
func (t *translator) statuses(now time.Time) []statusUpdate {
	at := now.UTC().Format(time.RFC3339)
	var out []statusUpdate
	add := func(kind string, m *meta, status any) {
		var p struct {
			Metadata struct {
				ResourceVersion string `json:"resourceVersion"`
			} `json:"metadata"`
			Status any `json:"status"`
		}
		p.Metadata.ResourceVersion, p.Status = m.ResourceVersion, status
		data, _ := json.Marshal(p)
		out = append(out, statusUpdate{kind: kind, ns: m.Namespace, name: m.Name, patch: data})
	}

	for _, c := range t.s.classes {
		if c.Spec.ControllerName != t.opts.Controller() {
			continue
		}
		want := settle([]Condition{cond("Accepted", true, "Accepted", "fit serves this class's Gateways", c.Metadata.Generation)},
			c.Status.Conditions, at)
		if !same(want, c.Status.Conditions) {
			add("gatewayclasses", &c.Metadata, map[string]any{"conditions": want})
		}
	}

	for _, gw := range t.gws {
		g := gw.g
		gen := g.Metadata.Generation
		// Addresses as opts give them, or as they are, when it gives none.
		want := GatewayStatus{Addresses: g.Status.Addresses}
		if len(t.opts.Addresses) > 0 {
			want.Addresses = nil
			for _, a := range t.opts.Addresses {
				typ := "Hostname"
				if net.ParseIP(a) != nil {
					typ = "IPAddress"
				}
				want.Addresses = append(want.Addresses, GatewayAddress{Type: ptr(typ), Value: a})
			}
		}
		accepted, programmed, valid := 0, 0, 0
		for _, gl := range gw.listeners {
			ls := ListenerStatus{Name: gl.l.Name, SupportedKinds: gl.kinds, AttachedRoutes: gl.attached}
			if ls.SupportedKinds == nil {
				ls.SupportedKinds = []RouteGroupKind{}
			}
			var have []Condition
			for _, h := range g.Status.Listeners {
				if h.Name == ls.Name {
					have = h.Conditions
				}
			}
			ls.Conditions = settle([]Condition{
				cond("Accepted", gl.refused == "", or(gl.refused, "Accepted"), gl.refusedMsg, gen),
				programmedCond(gl.fit != "", "fit does not serve it: see its other conditions", gen),
				cond("ResolvedRefs", gl.unresolved == "", or(gl.unresolved, "ResolvedRefs"), gl.unresolvedMsg, gen),
				cond("Conflicted", gl.conflict != "", or(gl.conflict, "NoConflicts"), gl.conflictMsg, gen),
			}, have, at)
			want.Listeners = append(want.Listeners, ls)
			if gl.refused == "" {
				accepted++
			}
			if gl.fit != "" {
				programmed++
			}
			if gl.refused == "" && gl.conflict == "" && gl.unresolved == "" {
				valid++
			}
		}
		reason := "Accepted"
		if valid < len(gw.listeners) {
			reason = "ListenersNotValid"
		}
		want.Conditions = settle([]Condition{
			cond("Accepted", accepted > 0, reason, "", gen),
			programmedCond(programmed > 0, "fit serves none of its listeners", gen),
		}, g.Status.Conditions, at)
		if !same(want, g.Status) {
			add("gateways", &g.Metadata, want)
		}
	}

	for _, st := range t.states {
		var mine []RouteParentStatus
		var others []json.RawMessage
		for _, raw := range st.status.Parents {
			var p RouteParentStatus
			if json.Unmarshal(raw, &p) == nil && p.ControllerName == t.opts.Controller() {
				p.ParentRef = normalRef(p.ParentRef)
				mine = append(mine, p)
			} else {
				others = append(others, raw)
			}
		}
		gen := st.m.Generation
		var want []RouteParentStatus
		for _, ps := range st.parents {
			if len(others)+len(want) == maxParents {
				break
			}
			p := RouteParentStatus{ParentRef: normalRef(ps.ref), ControllerName: t.opts.Controller(), Conditions: []Condition{
				cond("Accepted", ps.accepted, or(ps.reason, "Accepted"), ps.msg, gen),
				cond("ResolvedRefs", st.unresolved == "", or(st.unresolved, "ResolvedRefs"), st.unresolvedMsg, gen),
			}}
			if st.partial != "" {
				p.Conditions = append(p.Conditions, cond("PartiallyInvalid", true, "UnsupportedValue", st.partial, gen))
			}
			var have []Condition
			for _, h := range mine {
				if same(h.ParentRef, p.ParentRef) {
					have = h.Conditions
				}
			}
			p.Conditions = settle(p.Conditions, have, at)
			want = append(want, p)
		}
		if len(want) == 0 && len(mine) == 0 || same(want, mine) {
			continue
		}
		parents := others
		for _, p := range want {
			data, _ := json.Marshal(p)
			parents = append(parents, data)
		}
		if parents == nil {
			parents = []json.RawMessage{}
		}
		add(routePlurals[st.kind], st.m, map[string]any{"parents": parents})
	}
	return out
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// programmedCond returns a Programmed condition, with why not when not ok.
func programmedCond(ok bool, why string, gen int64) Condition {
	if ok {
		return cond("Programmed", true, "Programmed", "", gen)
	}
	return cond("Programmed", false, "Invalid", why, gen)
}

// cond returns a condition of type typ, True when ok, with no time yet.
func cond(typ string, ok bool, reason, msg string, gen int64) Condition {
	status := "False"
	if ok {
		status = "True"
	}
	return Condition{Type: typ, Status: status, Reason: reason, Message: msg, ObservedGeneration: gen}
}

// settle gives each of want's conditions the time of have's of its type
// when its status is the same, and at otherwise; it returns want.
func settle(want, have []Condition, at string) []Condition {
	for i := range want {
		want[i].LastTransitionTime = at
		for _, h := range have {
			if h.Type == want[i].Type && h.Status == want[i].Status && h.LastTransitionTime != "" {
				want[i].LastTransitionTime = h.LastTransitionTime
			}
		}
	}
	return want
}

// same reports whether a and b are the same in JSON.
func same(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

// normalRef returns ref with its group and kind written out, as the API
// server's defaults write them, so that refs compare as they are stored.
func normalRef(ref ParentRef) ParentRef {
	ref.Group, ref.Kind = ptr(deref(ref.Group, gatewayGroup)), ptr(deref(ref.Kind, "Gateway"))
	return ref
}

// objectPath returns the path of the object named name in namespace ns
// (none for a cluster's) of the kind whose path across namespaces is
// collection.
func objectPath(collection, ns, name string) string {
	if ns == "" {
		return collection + "/" + name
	}
	i := strings.LastIndex(collection, "/")
	return collection[:i] + "/namespaces/" + ns + collection[i:] + "/" + name
}
