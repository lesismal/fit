package admin

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/provider"
	"github.com/lesismal/fit/runtime"
)

// TestCheckWithoutConfig: a check of a Configs with no running version, or
// of a state that lacks a config, finds nothing, and says so with an empty
// list rather than null.
func TestCheckWithoutConfig(t *testing.T) {
	cs := &Configs{history: []*Version{nil}}
	if r := cs.Check(); r.Differences == nil || len(r.Differences) != 0 || r.ETag != "" || r.Version != 0 {
		t.Errorf("a check without a version: %+v", r)
	}
	if diffs := cs.checkLocked(checkState{cur: &Version{}}); diffs == nil || len(diffs) != 0 {
		t.Errorf("a check without a config: %#v", diffs)
	}
}

// TestCheckNothingRunning: a Configs whose runtime runs no graph and that
// has no server compares the config alone, and a config that agrees with
// itself has no differences: an empty list, not null.
func TestCheckNothingRunning(t *testing.T) {
	data := []byte(`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}]}`)
	c, err := config.Load(data)
	if err != nil {
		t.Fatal(err)
	}
	cs := NewConfigs(new(runtime.Runtime), nil, runtime.Env{}, c, data, SourceFile, 1)
	r := cs.Check()
	if r.Differences == nil || len(r.Differences) != 0 || r.Summary != (CheckSummary{}) || r.ETag != cs.Current().ETag {
		t.Errorf("a check of a config that runs nothing: %+v", r)
	}
}

// TestRunningRoles: the roles a manager runs are what a check compares the
// config with; a manager that runs none runs none.
func TestRunningRoles(t *testing.T) {
	m := app.NewManager(nil, nil, nil, nil)
	t.Cleanup(func() { m.Stop(context.Background()) })
	if got := runningRoles(m); len(got) != 0 {
		t.Errorf("the roles of a new manager: %v", got)
	}
	if got := runningRoles(nil); got != nil {
		t.Errorf("the roles without a manager: %v", got)
	}
}

// TestCheckSettingsEach: every restart-only setting the config changes is a
// drift of its own, whose fix brings back the value fit started with.
func TestCheckSettingsEach(t *testing.T) {
	first := &config.Config{}
	cfg := &config.Config{
		AccessLog: &config.AccessLog{Output: "stdout"},
		Telemetry: &config.Telemetry{Endpoint: "http://127.0.0.1:4318"},
		Engine:    &config.Engine{EventLoops: 2},
		ACME:      &config.ACME{Email: "ops@example.com"},
		Providers: []config.Provider{{Name: "p"}},
	}
	diffs := checkSettings(first, cfg)
	for _, name := range []string{"access_log", "telemetry", "engine", "acme", "providers"} {
		d := findDifference(diffs, "setting:"+name)
		if d == nil || d.Kind != kindDrift || d.Running != nil || d.Config == nil {
			t.Errorf("setting:%s: %+v", name, d)
			continue
		}
		if len(d.Fixes) != 1 || d.Fixes[0].Patch == nil || d.Fixes[0].Patch.Op != patchDelete || d.Fixes[0].Patch.Path != "/"+name {
			t.Errorf("setting:%s fixes: %s", name, driftPatch(d.Fixes))
		}
	}
	// The other way: fit started with them, and the config left them out.
	diffs = checkSettings(cfg, first)
	for _, name := range []string{"access_log", "telemetry", "engine", "acme", "providers"} {
		d := findDifference(diffs, "setting:"+name)
		if d == nil || d.Config != nil || d.Running == nil || len(d.Fixes) != 1 || d.Fixes[0].Patch.Op != patchReplace {
			t.Errorf("setting:%s, left out: %+v", name, d)
		}
	}
	if diffs := checkSettings(cfg, cfg); len(diffs) != 0 {
		t.Errorf("the same settings: %+v", diffs)
	}
}

// TestCheckProvidersSkips: what the check cannot place in the running
// config is left out: a member providers do not provide, a path met twice,
// one that is not a JSON pointer, and one the running config does not hold.
func TestCheckProvidersSkips(t *testing.T) {
	cur := &Version{
		baseSource: SourceFile,
		effective:  []byte(`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"name": "r1"}], "log": {}}`),
		sources: []provider.Source{
			{Path: "/listeners/0", Source: SourceFile},
			{Path: "/log", Source: "prov"},
			{Path: "/routes/0", Source: "prov"},
			{Path: "/routes/0", Source: "prov"},
			{Path: "/routes/~2", Source: "prov"},
			{Path: "/routes/7", Source: "prov"},
		},
	}
	diffs := checkProviders(cur)
	if len(diffs) != 1 || diffs[0].ID != "provider:prov:/routes/0" || diffs[0].Name != "r1" || diffs[0].Category != "routes" {
		t.Fatalf("the provided elements: %+v", diffs)
	}
	// A running config that does not parse has nothing of a provider's.
	if diffs := checkProviders(&Version{effective: []byte("{"), sources: cur.sources}); len(diffs) != 0 {
		t.Errorf("a running config that does not parse: %+v", diffs)
	}
	if got := providerMember("/log"); got != "" {
		t.Errorf("the member of /log: %q", got)
	}
}

// TestElementName: an element is known by its name, then by its handler's
// module, then by its path.
func TestElementName(t *testing.T) {
	for _, c := range []struct{ doc, want string }{
		{`{"name": "r1", "handler": {"module": "respond"}}`, "r1"},
		{`{"name": "", "handler": {"module": "respond"}}`, "respond"},
		{`{"name": 3, "handler": {"module": "proxy"}}`, "proxy"},
		{`{"handler": {"module": ""}}`, "/routes/0"},
		{`{"handler": {"module": 1}}`, "/routes/0"},
		{`{"handler": {}}`, "/routes/0"},
		{`{"handler": "respond"}`, "/routes/0"},
		{`{}`, "/routes/0"},
		{`"respond"`, "/routes/0"},
		{`[1]`, "/routes/0"},
	} {
		v, err := decodeJSON([]byte(c.doc))
		if err != nil {
			t.Fatal(err)
		}
		if got := elementName(v, "/routes/0"); got != c.want {
			t.Errorf("the name of %s: %q, want %q", c.doc, got, c.want)
		}
	}
}

// namedCerts is a certificate manager that serves the certificates it is
// given, as a listener's does.
type namedCerts []*tls.Certificate

func (namedCerts) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return nil, errors.New("no certificate")
}

func (c namedCerts) Certificates() []*tls.Certificate { return c }

// TestServedCertificates: the names a listener serves certificates for are
// their DNS names and IP addresses, or their common name when they have
// neither, deduped and sorted; a listener in the clear serves none.
func TestServedCertificates(t *testing.T) {
	leaf := func(cn string, dns []string, ips ...net.IP) *tls.Certificate {
		return &tls.Certificate{Leaf: &x509.Certificate{Subject: pkix.Name{CommonName: cn}, DNSNames: dns, IPAddresses: ips}}
	}
	listeners := []*runtime.Listener{
		{Name: "web", Certificates: namedCerts{
			leaf("ignored", []string{"b.example.com", "a.example.com"}, net.ParseIP("127.0.0.1")),
			leaf("", []string{"a.example.com"}),
			leaf("only-cn.example.com", nil),
			leaf("", nil),
			{},
		}},
		{Name: "plain"},
	}
	got := servedCertificates(listeners)
	want := map[string][]string{"web": {"127.0.0.1", "a.example.com", "b.example.com", "only-cn.example.com"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("served: %v, want %v", got, want)
	}
	if v := namesOrNil([]string{"a"}); !reflect.DeepEqual(v, []string{"a"}) {
		t.Errorf("names: %v", v)
	}
	if v := namesOrNil(nil); v != nil {
		t.Errorf("no names: %v", v)
	}
}

// TestCacheZoneParam: a cache middleware's zone is its zone param, or
// default when the param is empty or not a name.
func TestCacheZoneParam(t *testing.T) {
	for _, c := range []struct{ params, want string }{
		{`{"zone": "pages"}`, "pages"},
		{`{"zone": ""}`, "default"},
		{`{"zone": 5}`, "default"},
		{`{}`, "default"},
	} {
		v, err := config.ParseValue([]byte(c.params), "")
		if err != nil {
			t.Fatal(err)
		}
		if got := cacheZone(config.ModuleRef{Module: "cache", Params: v}); got != c.want {
			t.Errorf("the zone of %s: %q, want %q", c.params, got, c.want)
		}
	}
}

// TestBaseIndex: an element is found in the base config by its name, and
// is not found in a tree of another shape.
func TestBaseIndex(t *testing.T) {
	for _, c := range []struct {
		doc  string
		want int
	}{
		{`{"listeners": [{"name": "a"}, {"name": "web"}]}`, 1},
		{`{"listeners": [1, {"name": 2}, {"other": "web"}, {"name": "x"}]}`, -1},
		{`{"listeners": {"name": "web"}}`, -1},
		{`{"routes": []}`, -1},
		{`[{"name": "web"}]`, -1},
	} {
		base, err := decodeJSON([]byte(c.doc))
		if err != nil {
			t.Fatal(err)
		}
		if got := baseIndex(base, "listeners", "web"); got != c.want {
			t.Errorf("web in %s: %d, want %d", c.doc, got, c.want)
		}
	}
}

// TestBaseACMEName: a name is placed in a listener's tls.acme of the base
// config, and is not when the tree is of another shape.
func TestBaseACMEName(t *testing.T) {
	for _, c := range []struct {
		doc  string
		want acmeName
		ok   bool
	}{
		{`{"listeners": [{"name": "x"}, {"name": "web", "tls": {"acme": ["b", "a"]}}]}`, acmeName{1, 1}, true},
		{`{"listeners": [{"name": "web", "tls": {"acme": ["b", 1]}}]}`, acmeName{}, false},
		{`{"listeners": [{"name": "web", "tls": {"acme": "a"}}]}`, acmeName{}, false},
		{`{"listeners": [{"name": "web", "tls": {}}]}`, acmeName{}, false},
		{`{"listeners": [{"name": "web", "tls": "a"}]}`, acmeName{}, false},
		{`{"listeners": [{"name": "web"}]}`, acmeName{}, false},
		{`{"listeners": [{"name": "other"}]}`, acmeName{}, false},
	} {
		base, err := decodeJSON([]byte(c.doc))
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := baseACMEName(base, "web", "a"); got != c.want || ok != c.ok {
			t.Errorf("a on web in %s: %+v %v, want %+v %v", c.doc, got, ok, c.want, c.ok)
		}
	}
}

// TestMemberJSONEdges: nothing is no member, a value JSON cannot hold is
// none either, and a member that is not a pointer is set.
func TestMemberJSONEdges(t *testing.T) {
	if v := memberJSON(nil); v != nil {
		t.Errorf("nil: %v", v)
	}
	if v := memberJSON(make(chan int)); v != nil {
		t.Errorf("a channel: %v", v)
	}
	if v := memberJSON(config.Limits{MaxConns: 3}); v == nil || !strings.Contains(string(v.(json.RawMessage)), "3") {
		t.Errorf("a struct: %v", v)
	}
	if !memberSet(config.Limits{}) || !memberSet(3) {
		t.Error("a value that is not a pointer is not set")
	}
	if memberSet(nil) || memberSet([]string(nil)) {
		t.Error("nothing is set")
	}
}

// TestReconcileRequestErrors: a reconcile whose body is not JSON, or not
// sent as JSON, is refused before anything is checked.
func TestReconcileRequestErrors(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	etag := f.configs.Current().ETag
	if resp, body := f.send(t, "POST", "/config/reconcile", "text/plain", `{"fixes": []}`, etag); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("a body that is not JSON's type: %d %s", resp.StatusCode, body)
	}
	if resp, body := f.send(t, "POST", "/config/reconcile", "application/json", `{"fixes": `, etag); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "the body: ") {
		t.Errorf("a body that is not JSON: %d %s", resp.StatusCode, body)
	}
	if resp, body := f.send(t, "POST", "/config/reconcile", "application/json", `{"fixes": []}`, etag); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "at least one fix") {
		t.Errorf("no fixes: %d %s", resp.StatusCode, body)
	}
	if f.configs.Current().Number != 1 {
		t.Error("a refused reconcile applied a config")
	}
}

// TestApplyPatchErrors: a patch the base config no longer takes, or that is
// not one, is a bad request and changes nothing.
func TestApplyPatchErrors(t *testing.T) {
	tree := func() any {
		v, err := decodeJSON([]byte(`{"routes": [{"name": "r1"}], "log": {"level": "info"}, "n": 1}`))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct {
		name string
		p    ConfigPatch
		want string
	}{
		{"a path that is not a pointer", ConfigPatch{Op: patchDelete, Path: "routes"}, "not a JSON pointer"},
		{"an add of nothing", ConfigPatch{Op: patchAdd, Path: "/routes/-"}, "adds nothing"},
		{"an add that is not JSON", ConfigPatch{Op: patchAdd, Path: "/routes/-", Value: json.RawMessage("{")}, "not JSON"},
		{"an append to a scalar", ConfigPatch{Op: patchAdd, Path: "/n/x/-", Value: json.RawMessage(`1`)}, "is not an object"},
		{"a replace with nothing", ConfigPatch{Op: patchReplace, Path: "/log"}, "replaces it with nothing"},
		{"a replace that is not JSON", ConfigPatch{Op: patchReplace, Path: "/log", Value: json.RawMessage("[")}, "not JSON"},
		{"a delete of what is not there", ConfigPatch{Op: patchDelete, Path: "/routes/5"}, ""},
		{"an operation that is not one", ConfigPatch{Op: "move", Path: "/log"}, "not a patch operation"},
	} {
		_, err := applyPatch(tree(), &c.p)
		var re *RequestError
		if !errors.As(err, &re) || re.Status != http.StatusBadRequest || !strings.Contains(re.Msg, c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// TestMakeArray: an append makes the arrays and objects its path goes
// through, and is refused where the path meets something else.
func TestMakeArray(t *testing.T) {
	root, err := decodeJSON([]byte(`{"a": {"list": [1]}, "s": "x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if root, err = makeArray(root, []string{"b", "c", "list"}); err != nil {
		t.Fatal(err)
	}
	if root, err = makeArray(root, []string{"a", "list"}); err != nil {
		t.Fatal(err)
	}
	if got := string(encodeJSON(root)); strings.Join(strings.Fields(got), "") != `{"a":{"list":[1]},"s":"x","b":{"c":{"list":[]}}}` {
		t.Errorf("the tree: %s", got)
	}
	if _, err := makeArray(root, []string{"s", "list"}); err == nil || !strings.Contains(err.Error(), "/s is not an object") {
		t.Errorf("through a string: %v", err)
	}
	if _, err := makeArray(root, []string{"a"}); err == nil || !strings.Contains(err.Error(), "/a is not an array") {
		t.Errorf("an object for an array: %v", err)
	}
	if got, err := makeArray(root, nil); err != nil || got != root {
		t.Errorf("no path: %v", err)
	}
}

// TestSortDeletes: deletes go from the highest index of an array down,
// those of two arrays by the arrays' paths, a deeper one after, and a
// member's by its name; a path that is not a pointer keeps its place.
func TestSortDeletes(t *testing.T) {
	paths := func(ps []*ConfigPatch) []string {
		out := make([]string, len(ps))
		for i, p := range ps {
			out[i] = p.Path
		}
		return out
	}
	mk := func(paths ...string) []*ConfigPatch {
		out := make([]*ConfigPatch, len(paths))
		for i, p := range paths {
			out[i] = &ConfigPatch{Op: patchDelete, Path: p}
		}
		return out
	}
	ps := mk("/routes/0", "/upstreams/1", "/routes/2", "/listeners/0/tls/acme/0", "/log", "/listeners/1", "/acme")
	sortDeletes(ps)
	want := []string{"/acme", "/listeners/1", "/listeners/0/tls/acme/0", "/log", "/routes/2", "/routes/0", "/upstreams/1"}
	if got := paths(ps); !slices.Equal(got, want) {
		t.Errorf("sorted: %v, want %v", got, want)
	}
	// A delete in a later element of an array goes before an earlier
	// element's, whatever the paths' lengths; indices compare as numbers;
	// and what is inside an element goes before the element.
	ps = mk("/listeners/0", "/routes/9", "/listeners/1/tls/acme/0", "/routes/10", "/listeners/1")
	sortDeletes(ps)
	if got, want := paths(ps), []string{"/listeners/1/tls/acme/0", "/listeners/1", "/listeners/0", "/routes/10", "/routes/9"}; !slices.Equal(got, want) {
		t.Errorf("sorted: %v, want %v", got, want)
	}
	ps = mk("/b", "", "/a", "bad")
	sortDeletes(ps)
	if got := paths(ps); !slices.Equal(got, []string{"/b", "", "/a", "bad"}) {
		t.Errorf("paths that are not pointers moved: %v", got)
	}
}

// TestReconcileStaleBase: the fixes are checked against the config the
// edit changes, and a base config that no longer takes a selected patch, or
// does not parse, makes the reconcile fail with nothing applied.
func TestReconcileStaleBase(t *testing.T) {
	withLog := &config.Config{Log: &config.Log{Output: "stdout"}}
	cs := &Configs{first: &config.Config{}, currentCfg: withLog}
	// The config sets log, and fit started without: the fix deletes it, from
	// a base config that does not hold it.
	fix := []reconcileFix{{ID: "setting:log", Action: fixReplace}}
	_, err := cs.reconcile(&Version{data: []byte(`{"routes": []}`)}, fix)
	var re *RequestError
	if !errors.As(err, &re) || re.Status != http.StatusBadRequest || !strings.Contains(re.Msg, "no member") {
		t.Errorf("a delete the base config does not take: %v", err)
	}
	// fit started with log, and the config leaves it out: the fix writes
	// it back, into a base config that is not an object.
	cs = &Configs{first: withLog, currentCfg: &config.Config{}}
	if _, err := cs.reconcile(&Version{data: []byte(`[1]`)}, fix); !errors.As(err, &re) || re.Status != http.StatusBadRequest {
		t.Errorf("a replace the base config does not take: %v", err)
	}
	if _, err := cs.reconcile(&Version{data: []byte(`{`)}, fix); !errors.As(err, &re) || !strings.Contains(re.Msg, "not JSON") {
		t.Errorf("a base config that does not parse: %v", err)
	}
	// A patch the base config takes makes the new base config.
	out, err := cs.reconcile(&Version{data: []byte(`{"routes": []}`)}, fix)
	if err != nil || !strings.Contains(string(out), `"log"`) {
		t.Errorf("the fixed config: %v %s", err, out)
	}
}
