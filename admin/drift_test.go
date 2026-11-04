package admin

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/middleware/cache"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// driftCheck fetches GET /config/check and decodes its answer.
func driftCheck(t *testing.T, f *fixture) CheckResult {
	t.Helper()
	resp, body := f.get(t, "/config/check")
	if resp.StatusCode != 200 {
		t.Fatalf("GET /config/check: %d %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("GET /config/check: Content-Type %q", ct)
	}
	var r CheckResult
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("GET /config/check: %v: %s", err, body)
	}
	return r
}

// driftDifference returns the difference with id, or nil.
func driftDifference(r CheckResult, id string) *Difference {
	for i := range r.Differences {
		if r.Differences[i].ID == id {
			return &r.Differences[i]
		}
	}
	return nil
}

// driftFixes returns the selected fixes as the body of POST
// /config/reconcile takes.
func driftFixes(ids ...string) []map[string]string {
	fixes := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		action := "reapply"
		if i := strings.IndexByte(id, '='); i >= 0 {
			id, action = id[:i], id[i+1:]
		}
		fixes = append(fixes, map[string]string{"id": id, "action": action})
	}
	return fixes
}

// driftReconcile posts fixes to POST /config/reconcile with etag in If-Match.
func driftReconcile(t *testing.T, f *fixture, etag string, fixes []map[string]string) (*http.Response, string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"fixes": fixes})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", f.api+"/config/reconcile", strings.NewReader(string(data)))
	req.Header.Set("Content-Type", "application/json")
	if etag != "" {
		req.Header.Set("If-Match", etag)
	}
	return f.do(t, req)
}

// driftReconcileOne selects one fix of the difference id, action, with the
// check's ETag.
func driftReconcileOne(t *testing.T, f *fixture, r CheckResult, id, action string) (*http.Response, string) {
	t.Helper()
	return driftReconcile(t, f, r.ETag, []map[string]string{{"id": id, "action": action}})
}

// driftLoad loads doc and returns it with its config and its tree, as the
// check reads the base config.
func driftLoad(t *testing.T, doc string) (*config.Config, any) {
	t.Helper()
	c, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	base, err := decodeJSON([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return c, base
}

// driftPatch returns p as the API writes it, for comparison.
func driftPatch(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}

// TestCheckClean checks a fresh fit: nothing drifts, and a port 0 listener
// is reported as info.
func TestCheckClean(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	r := driftCheck(t, f)
	cur := f.configs.Current()
	if r.Version != cur.Number || r.ETag != cur.ETag || r.CheckedAt.IsZero() {
		t.Fatalf("check: version %d etag %q at %v, running version %d etag %q", r.Version, r.ETag, r.CheckedAt, cur.Number, cur.ETag)
	}
	if r.Summary.Drift != 0 || len(r.Differences) != r.Summary.Drift+r.Summary.Info {
		t.Fatalf("drift in a fresh fit: %+v", r)
	}
	port := driftDifference(r, "listener:web:port")
	if port == nil || port.Kind != "info" || port.Category != "listeners" || len(port.Fixes) != 0 {
		t.Fatalf("the port 0 listener: %+v", port)
	}
	if port.Config != "127.0.0.1:0" || port.Running == nil {
		t.Errorf("port 0 values: config %v running %v", port.Config, port.Running)
	}
	if r.Summary.Info != 1 {
		t.Errorf("info: %d, differences %+v", r.Summary.Info, r.Differences)
	}
}

// TestCheckSettings checks that a restart-only setting the config changed is
// a drift whose replace fix reverts it to the value in force.
func TestCheckSettings(t *testing.T) {
	withLog := func(output string) string {
		return `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "log": {"output": "` + output + `"},
  "routes": [{"handler": {"module": "respond"}}]}`
	}
	f := start(t, withLog("stdout"), &config.Admin{})
	firstLog := f.configs.first.Log
	// A log.level change takes effect on reload, so it is no drift.
	if _, err := f.configs.Apply([]byte(strings.Replace(withLog("stdout"), `"output": "stdout"`, `"level": "debug", "output": "stdout"`, 1)), SourceAdmin); err != nil {
		t.Fatal(err)
	}
	if r := driftCheck(t, f); r.Summary.Drift != 0 {
		t.Fatalf("a level change: %+v", r.Differences)
	}
	// A log output change takes a restart: the config disagrees with what
	// runs.
	if _, err := f.configs.Apply([]byte(withLog("stderr")), SourceAdmin); err != nil {
		t.Fatal(err)
	}
	r := driftCheck(t, f)
	d := driftDifference(r, "setting:log")
	if d == nil || d.Kind != "drift" || d.Category != "settings" || d.Config == nil || d.Running == nil {
		t.Fatalf("setting:log: %+v", d)
	}
	if len(d.Fixes) != 1 || d.Fixes[0].Action != "replace" || d.Fixes[0].Patch == nil {
		t.Fatalf("setting:log fixes: %+v", d.Fixes)
	}
	if p := d.Fixes[0].Patch; p.Op != "replace" || p.Path != "/log" || string(p.Value) != driftPatch(firstLog) {
		t.Errorf("setting:log patch: %s", driftPatch(p))
	}
	// The fix brings the config's log back to the one in force.
	resp, body := driftReconcileOne(t, f, r, "setting:log", "replace")
	if resp.StatusCode != 200 || resp.Header.Get("Fit-Config-Version") != fmt.Sprint(r.Version+1) {
		t.Fatalf("reconcile: %d %v %s", resp.StatusCode, resp.Header, body)
	}
	if r2 := driftCheck(t, f); r2.Summary.Drift != 0 {
		t.Fatalf("drift after the fix: %+v", r2.Differences)
	}
	var data map[string]any
	if _, body := f.get(t, "/config"); json.Unmarshal([]byte(body), &data) != nil {
		t.Fatalf("GET /config: %s", body)
	}
	if got := driftPatch(data["log"]); got != driftPatch(firstLog) {
		t.Errorf("the config's log after the fix: %s, want %s", got, driftPatch(firstLog))
	}
}

// TestCheckLimits checks that a limit the config sets, which takes effect
// only at start, is a drift whose fix sets the config to the value in force.
func TestCheckLimits(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	withLimits := `{"limits": {"max_conns": 1234}, "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"handler": {"module": "respond"}}]}`
	if _, err := f.configs.Apply([]byte(withLimits), SourceAdmin); err != nil {
		t.Fatal(err)
	}
	r := driftCheck(t, f)
	d := driftDifference(r, "setting:limits.max_conns")
	if d == nil || d.Kind != "drift" || d.Config != float64(1234) {
		t.Fatalf("setting:limits.max_conns: %+v", d)
	}
	engine := f.server.Settings().MaxConns
	if d.Running != float64(engine) {
		t.Errorf("running: %v, the engine's %d", d.Running, engine)
	}
	if len(d.Fixes) != 1 || d.Fixes[0].Action != "replace" || d.Fixes[0].Patch == nil ||
		d.Fixes[0].Patch.Path != "/limits/max_conns" || string(d.Fixes[0].Patch.Value) != fmt.Sprint(engine) {
		t.Fatalf("setting:limits.max_conns fixes: %s", driftPatch(d.Fixes))
	}
	if resp, body := driftReconcileOne(t, f, r, "setting:limits.max_conns", "replace"); resp.StatusCode != 200 {
		t.Fatalf("reconcile: %d %s", resp.StatusCode, body)
	}
	if r2 := driftCheck(t, f); driftDifference(r2, "setting:limits.max_conns") != nil {
		t.Fatalf("drift after the fix: %+v", r2.Differences)
	}
}

// TestCheckTLS checks the TLS rule: a name the base config writes and no
// certificate covers is drift, its fix removes it, and a name HTTPS by
// default added is info.
func TestCheckTLS(t *testing.T) {
	written := `{"listeners": [{"name": "web", "address": "127.0.0.1:0", "tls": {"acme": ["a.example.com"]}}],
  "routes": [{"handler": {"module": "respond"}}]}`
	c, base := driftLoad(t, written)
	diffs := checkTLS(c, nil, base)
	d := findDifference(diffs, "tls:web:a.example.com")
	if d == nil || d.Kind != "drift" || d.Config != "a.example.com" || len(tlsServed(d)) != 0 {
		t.Fatalf("a written name without a certificate: %+v", d)
	}
	if len(d.Fixes) != 1 || d.Fixes[0].Action != "delete" || d.Fixes[0].Patch == nil ||
		d.Fixes[0].Patch.Path != "/listeners/0/tls/acme/0" || d.Fixes[0].Patch.Op != "delete" {
		t.Fatalf("its fix: %s", driftPatch(d.Fixes))
	}
	// A certificate for a wildcard covers the name, and one for another name
	// is served but not asked for.
	served := map[string][]string{"web": {"*.example.com", "b.example.com"}}
	diffs = checkTLS(c, served, base)
	if findDifference(diffs, "tls:web:a.example.com") != nil {
		t.Errorf("a wildcard certificate does not cover the name: %+v", diffs)
	}
	if d := findDifference(diffs, "tls:web:served:b.example.com"); d == nil || d.Kind != "info" || len(d.Fixes) != 0 {
		t.Errorf("a certificate the config does not ask for: %+v", d)
	}
	// A name HTTPS by default added is info: the certificate is being
	// obtained.
	auto := `{"listeners": [{"name": "web", "address": "127.0.0.1:0", "tls": {}}],
  "routes": [{"match": {"host": ["a.example.com"]}, "handler": {"module": "respond"}}]}`
	c, base = driftLoad(t, auto)
	diffs = checkTLS(c, nil, base)
	if d := findDifference(diffs, "tls:web:a.example.com:auto"); d == nil || d.Kind != "info" || len(d.Fixes) != 0 {
		t.Errorf("a name HTTPS by default added: %+v", d)
	}
	// An IP address is covered by an exact name only.
	for _, tt := range []struct {
		served, name string
		want         bool
	}{
		{"a.example.com", "a.example.com", true},
		{"A.Example.com.", "a.example.com", true},
		{"*.example.com", "a.example.com", true},
		{"*.example.com", "example.com", false},
		{"*.example.com", "a.b.example.com", false},
		{"a.example.com", "b.example.com", false},
		{"127.0.0.1", "127.0.0.1", true},
	} {
		if got := coversCertificate(tt.served, tt.name); got != tt.want {
			t.Errorf("certificate %q covers %q: %v, want %v", tt.served, tt.name, got, tt.want)
		}
	}
}

// findDifference returns the difference with id from a rule's differences.
func findDifference(diffs []Difference, id string) *Difference {
	for i := range diffs {
		if diffs[i].ID == id {
			return &diffs[i]
		}
	}
	return nil
}

// tlsServed returns the served names a TLS difference reports, nil when it
// has none.
func tlsServed(d *Difference) []string {
	if d == nil || d.Running == nil {
		return nil
	}
	names, _ := d.Running.([]string)
	return names
}

// TestCheckTLSReconcile checks that the TLS fix removes the name from the
// base config and the drift goes with it.
func TestCheckTLSReconcile(t *testing.T) {
	f := start(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0", "tls": {"acme": ["a.example.com", "b.example.com"]}}],
  "routes": [{"match": {"host": ["c.example.com"]}, "handler": {"module": "respond"}}]}`, &config.Admin{})
	r := driftCheck(t, f)
	for _, name := range []string{"a.example.com", "b.example.com"} {
		if d := driftDifference(r, "tls:web:"+name); d == nil || d.Kind != "drift" {
			t.Fatalf("TLS drift for %s: %+v", name, r.Differences)
		}
	}
	// Both names go in one apply, from the higher index down, so that the
	// second delete does not move the first.
	resp, body := driftReconcile(t, f, r.ETag, driftFixes("tls:web:a.example.com=delete", "tls:web:b.example.com=delete"))
	if resp.StatusCode != 200 {
		t.Fatalf("reconcile: %d %s", resp.StatusCode, body)
	}
	if _, body := f.get(t, "/config"); strings.Contains(body, "a.example.com") || strings.Contains(body, "b.example.com") {
		t.Errorf("the config after the fix: %s", body)
	}
	r2 := driftCheck(t, f)
	if r2.Summary.Drift != 0 {
		t.Fatalf("drift after the fixes: %+v", r2.Differences)
	}
	// The route's host name, which HTTPS by default adds, is info: its
	// certificate is being obtained.
	if d := driftDifference(r2, "tls:web:c.example.com:auto"); d == nil || d.Kind != "info" {
		t.Errorf("the name HTTPS by default added: %+v", r2.Differences)
	}
}

// TestCheckListeners checks the listener rule with a graph and the addresses
// bound: a listener of the graph with none, an address the graph lacks, and
// a port 0 the kernel chose.
func TestCheckListeners(t *testing.T) {
	written := `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}, {"name": "gone", "address": "127.0.0.1:0"}]}`
	c, base := driftLoad(t, written)
	graph := []*runtime.Listener{{Name: "web", Address: "127.0.0.1:0"}, {Name: "gone", Address: "127.0.0.1:0"}}
	bound := []listener.Addr{
		{Name: "web", Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 41234}},
		{Name: "stray", Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 41235}},
	}
	diffs := checkListeners(c, graph, bound, base)
	if d := findDifference(diffs, "listener:gone:missing"); d == nil || d.Kind != "drift" ||
		len(d.Fixes) != 2 || d.Fixes[0].Action != "delete" || d.Fixes[0].Patch.Path != "/listeners/1" || d.Fixes[1].Action != "reapply" {
		t.Errorf("a listener with no bound address: %+v", d)
	}
	if d := findDifference(diffs, "listener:stray:extra"); d == nil || d.Kind != "drift" || d.Running != "127.0.0.1:41235" {
		t.Errorf("a bound listener the config lacks: %+v", d)
	}
	if d := findDifference(diffs, "listener:web:port"); d == nil || d.Kind != "info" || d.Config != "127.0.0.1:0" {
		t.Errorf("a port 0 listener: %+v", d)
	}
	// A config that writes no listener is served on the ones HTTPS by
	// default makes: they are no difference.
	auto, autoBase := driftLoad(t, `{"routes": [{"match": {"host": ["a.example.com"]}, "handler": {"module": "respond"}}]}`)
	listeners, _ := auto.Effective()
	if len(listeners) == 0 {
		t.Fatal("HTTPS by default made no listener")
	}
	var autoGraph []*runtime.Listener
	var autoBound []listener.Addr
	for _, l := range listeners {
		autoGraph = append(autoGraph, &runtime.Listener{Name: l.Name, Address: l.Address})
		autoBound = append(autoBound, listener.Addr{Name: l.Name, Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 80}})
	}
	if diffs := checkListeners(auto, autoGraph, autoBound, autoBase); len(diffs) != 0 {
		t.Errorf("the listeners HTTPS by default made: %+v", diffs)
	}
}

// TestCheckRoles checks the role rule through its helper: a role the config
// sets but is not running, and one running but not set.
func TestCheckRoles(t *testing.T) {
	role := app.Info{ID: "tunnel.node", Member: func(c *config.Config) any {
		if c.Tunnel == nil {
			return nil
		}
		return c.Tunnel.Node
	}}
	c, _ := driftLoad(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "tunnel": {"node": {}}}`)
	diffs := checkRoles(c, []app.Info{role}, nil)
	d := findDifference(diffs, "role:tunnel.node")
	if d == nil || d.Kind != "drift" || d.Config == nil || d.Running != false {
		t.Fatalf("a configured role that is not running: %+v", d)
	}
	if len(d.Fixes) != 2 || d.Fixes[0].Action != "delete" || d.Fixes[0].Patch == nil ||
		d.Fixes[0].Patch.Path != "/tunnel/node" || d.Fixes[0].Patch.Op != "delete" || d.Fixes[1].Action != "reapply" {
		t.Fatalf("its fixes: %s", driftPatch(d.Fixes))
	}
	if diffs := checkRoles(c, []app.Info{role}, []string{"tunnel.node"}); len(diffs) != 0 {
		t.Errorf("a role that is running: %+v", diffs)
	}
	empty, _ := driftLoad(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}]}`)
	diffs = checkRoles(empty, []app.Info{role}, []string{"tunnel.node"})
	d = findDifference(diffs, "role:tunnel.node:extra")
	if d == nil || d.Kind != "drift" || d.Config != nil || d.Running != true || len(d.Fixes) != 1 || d.Fixes[0].Action != "reapply" {
		t.Errorf("a role running but not configured: %+v", d)
	}
	// A nil pointer of a member's type is not a member.
	if memberSet((*config.TunnelNode)(nil)) {
		t.Error("a typed nil pointer is a member")
	}
	if !memberSet(&config.TunnelNode{}) {
		t.Error("a pointer is not a member")
	}
}

// TestReapply checks that a reapply fix applies the config again as a new
// version, and that the role's delete fix takes it out of the config.
func TestReapply(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	withRole := strings.TrimSuffix(doc("v2"), "}") + `, "tunnel": {"node": {}}}`
	if _, err := f.configs.Apply([]byte(withRole), SourceAdmin); err != nil {
		t.Fatal(err)
	}
	r := driftCheck(t, f)
	d := driftDifference(r, "role:tunnel.node")
	if d == nil || d.Kind != "drift" {
		t.Fatalf("the role: %+v", r.Differences)
	}
	etag := f.configs.Current().ETag
	resp, body := driftReconcileOne(t, f, r, "role:tunnel.node", "reapply")
	if resp.StatusCode != 200 || resp.Header.Get("Fit-Config-Version") != fmt.Sprint(r.Version+1) || resp.Header.Get("ETag") != etag {
		t.Fatalf("reapply: %d %v %s", resp.StatusCode, resp.Header, body)
	}
	if _, body := f.get(t, "/config"); body != withRole {
		t.Errorf("the config after a reapply: %q, want %q", body, withRole)
	}
	// The role still does not run: the fixture has no roles manager.
	if r2 := driftCheck(t, f); driftDifference(r2, "role:tunnel.node") == nil {
		t.Fatal("the role's drift went with no manager to start it")
	} else if resp, body := driftReconcileOne(t, f, r2, "role:tunnel.node", "delete"); resp.StatusCode != 200 {
		t.Fatalf("delete: %d %s", resp.StatusCode, body)
	}
	if r3 := driftCheck(t, f); r3.Summary.Drift != 0 {
		t.Fatalf("drift after the role's removal: %+v", r3.Differences)
	}
}

// TestCheckProviders checks that what a provider provides but the base
// config lacks is a drift, that its fix saves it in the base config, and
// that the effective config is the same after.
func TestCheckProviders(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	if err := f.configs.AddProvider("docker"); err != nil {
		t.Fatal(err)
	}
	part := `{"routes": [{"name": "pinned", "handler": {"module": "respond", "params": {"body": "p"}}}],
 "listeners": [{"name": "extra", "address": "127.0.0.1:0"}]}`
	if _, err := f.configs.Provide("docker", []byte(part)); err != nil {
		t.Fatal(err)
	}
	before := f.effective(t)
	r := driftCheck(t, f)
	for _, id := range []string{"provider:docker:/routes/1", "provider:docker:/listeners/1"} {
		d := driftDifference(r, id)
		if d == nil || d.Kind != "drift" || d.Config != nil || d.Running == nil ||
			len(d.Fixes) != 1 || d.Fixes[0].Action != "add" || d.Fixes[0].Patch == nil || d.Fixes[0].Patch.Op != "add" {
			t.Fatalf("%s: %s", id, driftPatch(d))
		}
	}
	if d := driftDifference(r, "provider:docker:/routes/1"); d.Category != "routes" || d.Name != "pinned" ||
		d.Fixes[0].Patch.Path != "/routes/-" {
		t.Errorf("the route's fix: %s", driftPatch(d.Fixes))
	} else {
		var want, got any
		if err := json.Unmarshal([]byte(`{"name": "pinned", "handler": {"module": "respond", "params": {"body": "p"}}}`), &want); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(d.Fixes[0].Patch.Value, &got); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("the value the fix saves: %s, want %s", d.Fixes[0].Patch.Value, driftPatch(want))
		}
	}
	resp, body := driftReconcile(t, f, r.ETag, driftFixes(
		"provider:docker:/routes/1=add", "provider:docker:/listeners/1=add"))
	if resp.StatusCode != 200 {
		t.Fatalf("reconcile: %d %s", resp.StatusCode, body)
	}
	if _, body := f.get(t, "/config"); !strings.Contains(body, `"pinned"`) || !strings.Contains(body, `"extra"`) {
		t.Errorf("the base config after the fix: %s", body)
	}
	after := f.effective(t)
	var b, a any
	if err := json.Unmarshal(before.Config, &b); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after.Config, &a); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b, a) {
		t.Errorf("the effective config changed:\nbefore %s\nafter  %s", before.Config, after.Config)
	}
	// The elements are the base config's now, so the provider's are
	// shadowed, which is info.
	r2 := driftCheck(t, f)
	if r2.Summary.Drift != 0 {
		t.Fatalf("drift after the fixes: %+v", r2.Differences)
	}
	for _, id := range []string{"shadowed:docker:pinned", "shadowed:docker:extra"} {
		if d := driftDifference(r2, id); d == nil || d.Kind != "info" || d.Category != "providers" || len(d.Fixes) != 0 {
			t.Errorf("%s: %+v", id, d)
		}
	}
}

// TestCheckShadowed checks that a provider's element left out because the
// name is taken is info.
func TestCheckShadowed(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	for _, name := range []string{"a", "b"} {
		if err := f.configs.AddProvider(name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.configs.Provide("a", []byte(`{"routes": [{"name": "shared", "handler": {"module": "respond"}}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.configs.Provide("b", []byte(`{"routes": [{"name": "shared", "handler": {"module": "respond"}}]}`)); err != nil {
		t.Fatal(err)
	}
	r := driftCheck(t, f)
	if d := driftDifference(r, "provider:a:/routes/1"); d == nil || d.Kind != "drift" {
		t.Errorf("the first provider's route: %+v", d)
	}
	d := driftDifference(r, "shadowed:b:shared")
	if d == nil || d.Kind != "info" || d.Category != "providers" || d.Name != "shared" || len(d.Fixes) != 0 {
		t.Errorf("the other provider's route: %+v", d)
	}
}

// TestCheckUpstreams checks the upstream rule: a pool the config lacks, a
// config upstream with no pool, and a pool whose backends are all down.
func TestCheckUpstreams(t *testing.T) {
	c, base := driftLoad(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "upstreams": [{"name": "a", "addresses": ["127.0.0.1:1"]}, {"name": "c", "addresses": ["127.0.0.1:2"]}]}`)
	a, err := upstream.New(nil, &c.Upstreams[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	b, err := upstream.New(nil, &config.Upstream{Name: "b", Addresses: []string{"127.0.0.1:3"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	diffs := checkUpstreams(c, []*upstream.Pool{a, b}, base)
	if d := findDifference(diffs, "upstream:c:missing"); d == nil || d.Kind != "drift" || len(d.Fixes) != 2 ||
		d.Fixes[0].Action != "delete" || d.Fixes[0].Patch.Path != "/upstreams/1" || d.Fixes[1].Action != "reapply" {
		t.Errorf("a config upstream with no pool: %+v", d)
	}
	if d := findDifference(diffs, "upstream:b:extra"); d == nil || d.Kind != "drift" || len(d.Fixes) != 1 || d.Fixes[0].Action != "reapply" {
		t.Errorf("a pool the config lacks: %+v", d)
	}
	// A failed request takes a's only backend out of rotation.
	req, _ := http.NewRequest("GET", "http://example.com/", nil)
	a.Do(req, func(*fibhttp.ClientResponse, error) {})
	if !a.Backends()[0].Down() {
		t.Fatal("the backend is not down after a failed request")
	}
	diffs = checkUpstreams(c, []*upstream.Pool{a}, base)
	d := findDifference(diffs, "upstream:a:down")
	if d == nil || d.Kind != "info" || len(d.Fixes) != 0 {
		t.Fatalf("a pool whose backends are down: %+v", d)
	}
	states, ok := d.Running.([]backendState)
	if !ok || len(states) != 1 || states[0].Address != "127.0.0.1:1" || states[0].Up || states[0].InFlight != 0 {
		t.Errorf("the backends: %+v", d.Running)
	}
}

// TestCheckCache checks the cache rule: a zone a route uses and no pool
// holds is drift, and a zone kept with no use is info.
func TestCheckCache(t *testing.T) {
	withCache := `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"middleware": [{"module": "cache", "params": {"zone": "i121-kept"}}],
    "handler": {"module": "respond", "params": {"body": "x"}}}]}`
	c, _ := driftLoad(t, withCache)
	if diffs := checkCache(c, nil); findDifference(diffs, "cache:zone:i121-kept:missing") == nil {
		t.Errorf("a zone no route has: %+v", diffs)
	}
	diffs := checkCache(c, []cache.ZoneStats{{Zone: "i121-kept", Keys: 2, Bytes: 100}})
	for _, d := range diffs {
		if d.Kind == "drift" {
			t.Errorf("a zone that is open drifts: %+v", d)
		}
	}
	c, _ = driftLoad(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}]}`)
	diffs = checkCache(c, []cache.ZoneStats{{Zone: "kept", Keys: 2, Bytes: 100, DiskEntries: 3, DiskBytes: 400}})
	d := findDifference(diffs, "cache:zone:kept:kept")
	if d == nil || d.Kind != "info" || len(d.Fixes) != 0 {
		t.Fatalf("a kept zone: %+v", d)
	}
	if states, ok := d.Running.(zoneState); !ok || states.Keys != 2 || states.Bytes != 100 || states.DiskEntries != 3 || states.DiskBytes != 400 {
		t.Errorf("what the zone keeps: %+v", d.Running)
	}
	// A zone a config applied before still holds is kept, and info.
	f := start(t, withCache, &config.Admin{})
	// Hold the graph the cache is provisioned in, as a request under way
	// would, so that a reload leaves its zone kept.
	g := f.configs.rt.Current()
	defer g.Release()
	if _, err := f.configs.Apply([]byte(doc("v2")), SourceAdmin); err != nil {
		t.Fatal(err)
	}
	r := driftCheck(t, f)
	if d := driftDifference(r, "cache:zone:i121-kept:kept"); d == nil || d.Kind != "info" || len(d.Fixes) != 0 {
		t.Errorf("a kept zone: %+v", d)
	}
	if r.Summary.Drift != 0 {
		t.Errorf("drift without a change: %+v", r.Differences)
	}
}

// TestCheckEndpoint checks GET /config/check's shape and order, and the
// statuses POST /config/reconcile answers.
func TestCheckEndpoint(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	// A config with a few differences, so that their order shows.
	with := `{"limits": {"max_conns": 7}, "log": {"output": "stderr"},
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"handler": {"module": "respond"}}]}`
	if _, err := f.configs.Apply([]byte(with), SourceAdmin); err != nil {
		t.Fatal(err)
	}
	r := driftCheck(t, f)
	if r.Version != f.configs.Current().Number || r.ETag != f.configs.Current().ETag {
		t.Fatalf("version %d etag %q", r.Version, r.ETag)
	}
	var ids []string
	for _, d := range r.Differences {
		if d.Kind != "drift" && d.Kind != "info" {
			t.Errorf("a difference's kind: %q", d.Kind)
		}
		ids = append(ids, d.ID)
	}
	// Settings come first, their limits before log by name, then the
	// listener's port.
	want := []string{"setting:limits.max_conns", "setting:log", "listener:web:port"}
	for i, id := range want {
		if i >= len(ids) || ids[i] != id {
			t.Fatalf("the differences in order: %v, want %v first", ids, want)
		}
	}
	if r.Summary.Drift != 2 || r.Summary.Info != 1 {
		t.Errorf("summary: %+v of %+v", r.Summary, r.Differences)
	}
	// The same check twice lists the same differences: only the time
	// differs.
	r2 := driftCheck(t, f)
	if !slices.Equal(ids, differenceIDs(r2)) {
		t.Errorf("a second check: %v, then %v", ids, differenceIDs(r2))
	}

	// If-Match is needed, and a stale one is 412.
	if resp, body := driftReconcile(t, f, "", driftFixes("setting:log=replace")); resp.StatusCode != http.StatusPreconditionRequired {
		t.Errorf("without If-Match: %d %s", resp.StatusCode, body)
	}
	if resp, body := driftReconcile(t, f, `"stale"`, driftFixes("setting:log=replace")); resp.StatusCode != http.StatusPreconditionFailed ||
		!strings.Contains(body, "the config has changed") {
		t.Errorf("a stale If-Match: %d %s", resp.StatusCode, body)
	}
	if f.configs.Current().Number != r.Version {
		t.Error("a refused reconcile applied a config")
	}

	// An empty fixes list is bad, and so is one whose difference is gone.
	if resp, body := driftReconcile(t, f, r.ETag, nil); resp.StatusCode != 400 || !strings.Contains(body, "name at least one fix") {
		t.Errorf("no fixes: %d %s", resp.StatusCode, body)
	}
	if resp, body := driftReconcile(t, f, r.ETag, driftFixes("listener:nope:missing=delete")); resp.StatusCode != 400 ||
		!strings.Contains(body, "is not there any more: check again") {
		t.Errorf("a difference that is gone: %d %s", resp.StatusCode, body)
	}
	// An action the difference does not offer is bad.
	if resp, body := driftReconcile(t, f, r.ETag, driftFixes("setting:log=delete")); resp.StatusCode != 400 ||
		!strings.Contains(body, "offers no") {
		t.Errorf("an action not offered: %d %s", resp.StatusCode, body)
	}
	// The same difference twice is bad too.
	if resp, body := driftReconcile(t, f, r.ETag, driftFixes("setting:log=replace", "setting:log=replace")); resp.StatusCode != 400 ||
		!strings.Contains(body, "is named more than once") {
		t.Errorf("a difference named twice: %d %s", resp.StatusCode, body)
	}
}

// TestCheckSettingsRemoved checks that a restart-only setting in force but
// left out of the config is a drift whose fix brings it back, and that admin,
// which comes from the bootstrap file, is info.
func TestCheckSettingsRemoved(t *testing.T) {
	f := start(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "log": {"output": "stdout"}}`, &config.Admin{})
	if _, err := f.configs.Apply([]byte(doc("v2")), SourceAdmin); err != nil {
		t.Fatal(err)
	}
	r := driftCheck(t, f)
	d := driftDifference(r, "setting:log")
	if d == nil || d.Kind != "drift" || d.Config != nil || d.Running == nil {
		t.Fatalf("setting:log: %+v", d)
	}
	if len(d.Fixes) != 1 || d.Fixes[0].Patch == nil || d.Fixes[0].Patch.Op != "replace" || d.Fixes[0].Patch.Path != "/log" {
		t.Fatalf("setting:log fixes: %s", driftPatch(d.Fixes))
	}
	if resp, body := driftReconcileOne(t, f, r, "setting:log", "replace"); resp.StatusCode != 200 {
		t.Fatalf("reconcile: %d %s", resp.StatusCode, body)
	}
	if _, body := f.get(t, "/config"); !strings.Contains(body, `"log"`) {
		t.Errorf("the config after the fix: %s", body)
	}
	if r2 := driftCheck(t, f); r2.Summary.Drift != 0 {
		t.Fatalf("drift after the fix: %+v", r2.Differences)
	}
	// admin needs a restart, so a change to it is info, with no fix.
	withAdmin := `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "log": {"output": "stdout"}, "admin": {"address": "127.0.0.1:1"}}`
	if _, err := f.configs.Apply([]byte(withAdmin), SourceAdmin); err != nil {
		t.Fatal(err)
	}
	d = driftDifference(driftCheck(t, f), "setting:admin")
	if d == nil || d.Kind != "info" || len(d.Fixes) != 0 {
		t.Fatalf("setting:admin: %+v", d)
	}
}

// TestCheckSettingDelete checks the other direction: a setting the config
// sets but fit started without is a drift whose fix deletes it.
func TestCheckSettingDelete(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	withLog := `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "log": {"output": "stdout"},
  "routes": [{"handler": {"module": "respond"}}]}`
	if _, err := f.configs.Apply([]byte(withLog), SourceAdmin); err != nil {
		t.Fatal(err)
	}
	r := driftCheck(t, f)
	d := driftDifference(r, "setting:log")
	if d == nil || d.Kind != "drift" || d.Running != nil {
		t.Fatalf("setting:log: %+v", d)
	}
	if len(d.Fixes) != 1 || d.Fixes[0].Patch == nil || d.Fixes[0].Patch.Op != "delete" || d.Fixes[0].Patch.Path != "/log" {
		t.Fatalf("setting:log fixes: %s", driftPatch(d.Fixes))
	}
	if resp, body := driftReconcileOne(t, f, r, "setting:log", "replace"); resp.StatusCode != 200 {
		t.Fatalf("reconcile: %d %s", resp.StatusCode, body)
	}
	if _, body := f.get(t, "/config"); strings.Contains(body, `"log"`) {
		t.Errorf("the config still sets log: %s", body)
	}
	if r2 := driftCheck(t, f); r2.Summary.Drift != 0 {
		t.Fatalf("drift after the fix: %+v", r2.Differences)
	}
}

// TestReconcileCreatesArray checks that a fix which appends to an array the
// base config leaves out makes the array first.
func TestReconcileCreatesArray(t *testing.T) {
	f := start(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}]}`, &config.Admin{})
	if err := f.configs.AddProvider("prov"); err != nil {
		t.Fatal(err)
	}
	part := `{"routes": [{"name": "r1", "handler": {"module": "respond", "params": {"body": "1"}}},
 {"name": "r2", "handler": {"module": "respond", "params": {"body": "2"}}}]}`
	if _, err := f.configs.Provide("prov", []byte(part)); err != nil {
		t.Fatal(err)
	}
	r := driftCheck(t, f)
	if driftDifference(r, "provider:prov:/routes/0") == nil || driftDifference(r, "provider:prov:/routes/1") == nil {
		t.Fatalf("the provider's routes: %+v", r.Differences)
	}
	resp, body := driftReconcile(t, f, r.ETag, driftFixes("provider:prov:/routes/0=add", "provider:prov:/routes/1=add"))
	if resp.StatusCode != 200 {
		t.Fatalf("reconcile: %d %s", resp.StatusCode, body)
	}
	if _, body := f.get(t, "/config"); !strings.Contains(body, `"routes"`) ||
		!strings.Contains(body, `"r1"`) || !strings.Contains(body, `"r2"`) {
		t.Errorf("the config after the fixes: %s", body)
	}
	if r2 := driftCheck(t, f); r2.Summary.Drift != 0 {
		t.Fatalf("drift after the fixes: %+v", r2.Differences)
	}
}

// TestReconcileInvalid checks that nothing is applied when the config the
// fixes make does not load.
func TestReconcileInvalid(t *testing.T) {
	f := start(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0", "tls": {"acme": ["a.example.com"]}}],
  "routes": [{"handler": {"module": "respond"}}]}`, &config.Admin{})
	r := driftCheck(t, f)
	if driftDifference(r, "tls:web:a.example.com") == nil {
		t.Fatalf("the TLS drift: %+v", r.Differences)
	}
	// The one name gone leaves a listener with TLS and nothing to serve.
	resp, body := driftReconcileOne(t, f, r, "tls:web:a.example.com", "delete")
	if resp.StatusCode != 400 || !strings.Contains(body, "the config was not applied; the running config stays") {
		t.Fatalf("reconcile: %d %s", resp.StatusCode, body)
	}
	if f.configs.Current().Number != r.Version {
		t.Error("a config that does not load was applied")
	}
	if _, body := f.get(t, "/config"); !strings.Contains(body, "a.example.com") {
		t.Errorf("the config changed: %s", body)
	}
}

// differenceIDs returns the IDs of a check's differences.
func differenceIDs(r CheckResult) []string {
	ids := make([]string, len(r.Differences))
	for i, d := range r.Differences {
		ids[i] = d.ID
	}
	return ids
}
