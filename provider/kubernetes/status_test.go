package kubernetes

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// statusWorld is a Gateway of fit's with listeners valid and not, and
// routes it takes and refuses.
func statusWorld(t *testing.T) *world {
	w := base(t)
	w.gateways = []*Gateway{obj[Gateway](t, `{"metadata": {"namespace": "default", "name": "gw", "generation": 3, "resourceVersion": "7"},
  "spec": {"gatewayClassName": "fit", "listeners": [
  {"name": "http", "port": 80, "protocol": "HTTP", "allowedRoutes": {"namespaces": {"from": "All"}}},
  {"name": "web", "port": 8080, "protocol": "HTTP", "hostname": "*.example.com"},
  {"name": "tls", "port": 443, "protocol": "HTTPS"},
  {"name": "odd", "port": 9, "protocol": "SCTP"},
  {"name": "kinds", "port": 81, "protocol": "HTTP", "allowedRoutes": {"kinds": [{"kind": "TCPRoute"}]}}]},
  "status": {"conditions": [{"type": "Accepted", "status": "Unknown", "reason": "Pending", "message": "", "lastTransitionTime": "1970-01-01T00:00:00Z"}]}}`)}
	route := func(ns, name, spec string, extra string) *HTTPRoute {
		return obj[HTTPRoute](t, `{"metadata": {"namespace": "`+ns+`", "name": "`+name+`", "generation": 1, "resourceVersion": "1"}, "spec": `+spec+extra+`}`)
	}
	w.routes = []*HTTPRoute{
		route("default", "ok", `{"parentRefs": [{"name": "gw", "sectionName": "http"}], "rules": [{"backendRefs": [{"name": "web", "port": 80}]}]}`,
			`, "status": {"parents": [{"parentRef": {"name": "elsewhere"}, "controllerName": "example.net/other", "conditions": []}]}`),
		route("default", "nosection", `{"parentRefs": [{"name": "gw", "sectionName": "nope"}], "rules": [{}]}`, ""),
		route("default", "host", `{"parentRefs": [{"name": "gw", "sectionName": "web"}], "hostnames": ["x.example.org"], "rules": [{}]}`, ""),
		route("outside", "far", `{"parentRefs": [{"namespace": "default", "name": "gw", "sectionName": "web"}], "rules": [{}]}`, ""),
		route("default", "broken", `{"parentRefs": [{"name": "gw", "port": 80}], "rules": [
  {"matches": [{"headers": [{"type": "RegularExpression", "name": "X", "value": ".*"}]}], "backendRefs": [{"name": "nothere", "port": 80}]}]}`, ""),
		// No longer on fit's Gateway: its entry of fit's goes, the other's
		// stays.
		route("default", "stale", `{"parentRefs": [{"name": "theirs"}], "rules": [{}]}`,
			`, "status": {"parents": [{"parentRef": {"group": "gateway.networking.k8s.io", "kind": "Gateway", "name": "gw"}, "controllerName": "lesismal.github.io/fit", "conditions": []},
  {"parentRef": {"name": "theirs"}, "controllerName": "example.net/other", "conditions": []}]}`),
		// Not on fit's Gateway, and never was: no status of fit's.
		route("default", "theirs", `{"parentRefs": [{"name": "theirs"}], "rules": [{}]}`, ""),
	}
	return w
}

// patches returns the statuses of w after a translation, by kind and key,
// decoded.
func patches(t *testing.T, w *world, now time.Time) map[string]map[string]any {
	t.Helper()
	tr := newTranslator("k8s", &config.KubernetesProvider{}, w.snapshot(), "")
	tr.run()
	out := map[string]map[string]any{}
	for _, u := range tr.statuses(now) {
		var p map[string]any
		if err := json.Unmarshal(u.patch, &p); err != nil {
			t.Fatal(err)
		}
		out[u.kind+" "+u.ns+"/"+u.name] = p
	}
	return out
}

// apply gives w's objects the statuses ps, as the API server would.
func apply(t *testing.T, w *world, ps map[string]map[string]any) {
	t.Helper()
	for key, p := range ps {
		data, _ := json.Marshal(p["status"])
		kind, name, _ := strings.Cut(key, " ")
		var target any
		switch kind {
		case "gatewayclasses":
			for _, c := range w.classes {
				if "/"+c.Metadata.Name == name {
					target = &c.Status
				}
			}
		case "gateways":
			for _, g := range w.gateways {
				if g.Metadata.key() == name {
					target = &g.Status
				}
			}
		case "httproutes":
			for _, r := range w.routes {
				if r.Metadata.key() == name {
					r.Status = RouteStatus{}
					target = &r.Status
				}
			}
		}
		if target == nil {
			t.Fatalf("no object for %s", key)
		}
		if err := json.Unmarshal(data, target); err != nil {
			t.Fatal(err)
		}
	}
}

// conds returns the conditions at path in v, by type, as
// "status reason message lastTransitionTime".
func conds(t *testing.T, v any) map[string]string {
	t.Helper()
	out := map[string]string{}
	list, _ := v.([]any)
	for _, c := range list {
		m := c.(map[string]any)
		out[m["type"].(string)] = strings.Join([]string{m["status"].(string), m["reason"].(string), m["message"].(string),
			m["lastTransitionTime"].(string)}, " ")
		if g, ok := m["observedGeneration"]; ok {
			out[m["type"].(string)] += " gen" + toJSON(g)
		}
	}
	return out
}

func parentsOf(p map[string]any) []map[string]any {
	var out []map[string]any
	for _, x := range p["status"].(map[string]any)["parents"].([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

func TestStatus(t *testing.T) {
	w := statusWorld(t)
	t0 := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	ps := patches(t, w, t0)
	const at0 = "2026-10-22T10:00:00Z"

	// fit's class is accepted; the other's is not written.
	if c := conds(t, ps["gatewayclasses /fit"]["status"].(map[string]any)["conditions"]); c["Accepted"] != "True Accepted fit serves this class's Gateways "+at0 {
		t.Errorf("class: %v", c)
	}
	if _, ok := ps["gatewayclasses /other"]; ok {
		t.Error("another controller's class is written")
	}

	g := ps["gateways default/gw"]
	if rv := g["metadata"].(map[string]any)["resourceVersion"]; rv != "7" {
		t.Errorf("the patch names resource version %v", rv)
	}
	gs := g["status"].(map[string]any)
	if c := conds(t, gs["conditions"]); c["Accepted"] != "True ListenersNotValid  "+at0+" gen3" || c["Programmed"] != "True Programmed  "+at0+" gen3" {
		t.Errorf("gateway: %v", c)
	}
	listeners := map[string]map[string]any{}
	for _, l := range gs["listeners"].([]any) {
		listeners[l.(map[string]any)["name"].(string)] = l.(map[string]any)
	}
	for name, want := range map[string]struct {
		kinds    string
		attached float64
		conds    map[string]string
	}{
		"http": {`[{"group":"gateway.networking.k8s.io","kind":"HTTPRoute"},{"group":"gateway.networking.k8s.io","kind":"GRPCRoute"}]`, 2, map[string]string{
			"Accepted": "True Accepted", "Programmed": "True Programmed", "ResolvedRefs": "True ResolvedRefs", "Conflicted": "False NoConflicts"}},
		"web": {`[{"group":"gateway.networking.k8s.io","kind":"HTTPRoute"},{"group":"gateway.networking.k8s.io","kind":"GRPCRoute"}]`, 0, map[string]string{
			"Accepted": "True Accepted", "Programmed": "True Programmed"}},
		"tls": {`[{"group":"gateway.networking.k8s.io","kind":"HTTPRoute"},{"group":"gateway.networking.k8s.io","kind":"GRPCRoute"}]`, 0, map[string]string{
			"Accepted": "True Accepted", "Programmed": "False Invalid", "ResolvedRefs": "False InvalidCertificateRef"}},
		"odd":   {`[]`, 0, map[string]string{"Accepted": "False UnsupportedProtocol", "Programmed": "False Invalid"}},
		"kinds": {`[]`, 0, map[string]string{"Accepted": "True Accepted", "ResolvedRefs": "False InvalidRouteKinds"}},
	} {
		l := listeners[name]
		if got := toJSON(l["supportedKinds"]); got != want.kinds || l["attachedRoutes"] != want.attached {
			t.Errorf("listener %s: kinds %s, %v attached", name, got, l["attachedRoutes"])
		}
		c := conds(t, l["conditions"])
		for typ, prefix := range want.conds {
			if !strings.HasPrefix(c[typ], prefix+" ") {
				t.Errorf("listener %s: %s is %q, want %s", name, typ, c[typ], prefix)
			}
		}
	}

	// Routes: on fit's Gateway, accepted or not and why; their backends.
	for name, want := range map[string]map[string]string{
		"ok":        {"Accepted": "True Accepted", "ResolvedRefs": "True ResolvedRefs"},
		"nosection": {"Accepted": "False NoMatchingParent"},
		"host":      {"Accepted": "False NoMatchingListenerHostname"},
		"far":       {"Accepted": "False NotAllowedByListeners"},
		"broken":    {"Accepted": "True Accepted", "ResolvedRefs": "False BackendNotFound service default/nothere does not exist", "PartiallyInvalid": "True UnsupportedValue"},
	} {
		key := "httproutes default/" + name
		if name == "far" {
			key = "httproutes outside/far"
		}
		p, ok := ps[key]
		if !ok {
			t.Errorf("%s: no status", name)
			continue
		}
		parents := parentsOf(p)
		mine := parents[len(parents)-1]
		if mine["controllerName"] != "lesismal.github.io/fit" || mine["parentRef"].(map[string]any)["group"] != "gateway.networking.k8s.io" {
			t.Errorf("%s: %v", name, mine)
		}
		c := conds(t, mine["conditions"])
		for typ, prefix := range want {
			if !strings.HasPrefix(c[typ], prefix) || !strings.HasSuffix(c[typ], at0+" gen1") {
				t.Errorf("%s: %s is %q, want %s", name, typ, c[typ], prefix)
			}
		}
		if _, has := c["PartiallyInvalid"]; has != (name == "broken") {
			t.Errorf("%s: %v", name, c)
		}
	}
	// ok keeps the other controller's entry before its own.
	if parents := parentsOf(ps["httproutes default/ok"]); len(parents) != 2 || parents[0]["controllerName"] != "example.net/other" {
		t.Errorf("ok's parents: %v", parents)
	}
	if parents := parentsOf(ps["httproutes default/stale"]); len(parents) != 1 || parents[0]["controllerName"] != "example.net/other" {
		t.Errorf("stale's parents: %v", parents)
	}
	if _, ok := ps["httproutes default/theirs"]; ok {
		t.Error("a route never on fit's Gateways is written")
	}

	// Once written, the same objects need nothing more.
	apply(t, w, ps)
	if again := patches(t, w, t0.Add(time.Hour)); len(again) != 0 {
		t.Fatalf("written again: %v", again)
	}

	// A backend gone: ResolvedRefs changes, and its time with it; Accepted
	// keeps its own.
	w.routes[0].Spec.Rules[0].BackendRefs[0].Name = "gone"
	ps = patches(t, w, t0.Add(time.Hour))
	if len(ps) != 1 {
		t.Fatalf("after a change: %v", ps)
	}
	parents := parentsOf(ps["httproutes default/ok"])
	c := conds(t, parents[1]["conditions"])
	if c["Accepted"] != "True Accepted  "+at0+" gen1" || !strings.HasPrefix(c["ResolvedRefs"], "False BackendNotFound") ||
		!strings.Contains(c["ResolvedRefs"], "2026-10-22T11:00:00Z") {
		t.Errorf("after a change: %v", c)
	}
}

func TestObjectPath(t *testing.T) {
	for _, c := range [][4]string{
		{gatewayAPI + "v1/gatewayclasses", "", "fit", gatewayAPI + "v1/gatewayclasses/fit"},
		{gatewayAPI + "v1/gateways", "default", "gw", gatewayAPI + "v1/namespaces/default/gateways/gw"},
		{gatewayAPI + "v1alpha2/tlsroutes", "team", "db", gatewayAPI + "v1alpha2/namespaces/team/tlsroutes/db"},
	} {
		if got := objectPath(c[0], c[1], c[2]); got != c[3] {
			t.Errorf("%v: %s", c, got)
		}
	}
}
