package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	_ "github.com/lesismal/fit/handler/respond"
)

func mustParse(t *testing.T, name, data string) *Part {
	t.Helper()
	p, err := Parse(name, []byte(data))
	if err != nil {
		t.Fatalf("Parse %s: %v", name, err)
	}
	return p
}

// names returns the names, or "-" for none, of the elements of each kind
// in a merged config.
func names(t *testing.T, data []byte) map[string][]string {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("the merged config is not JSON: %v\n%s", err, data)
	}
	out := map[string][]string{}
	for _, k := range Kinds {
		var elems []struct {
			Name string `json:"name"`
		}
		if raw, ok := doc[k]; ok {
			if err := json.Unmarshal(raw, &elems); err != nil {
				t.Fatal(err)
			}
		}
		for _, e := range elems {
			if e.Name == "" {
				e.Name = "-"
			}
			out[k] = append(out[k], e.Name)
		}
	}
	return out
}

func TestMerge(t *testing.T) {
	base := `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"handler": {"module": "respond"}}],
  "upstreams": [{"name": "api", "addresses": ["127.0.0.1:1"]}],
  "limits": {}
}`
	docker := mustParse(t, "docker", `{
  "upstreams": [{"name": "api", "addresses": ["127.0.0.1:2"]}, {"name": "web", "addresses": ["127.0.0.1:3"]}],
  "routes": [{"name": "d", "handler": {"module": "respond"}}, {"handler": {"module": "respond"}}],
  "services": [{"name": "tcp", "addresses": ["127.0.0.1:4"]}]
}`)
	consul := mustParse(t, "consul", `{
  "upstreams": [{"name": "web", "addresses": ["127.0.0.1:5"]}, {"name": "db", "addresses": ["127.0.0.1:6"]}],
  "routes": [{"name": "d", "handler": {"module": "respond"}}],
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}, {"name": "alt", "address": "127.0.0.1:0"}]
}`)
	m, err := Merge([]byte(base), "file", []*Part{docker, consul})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"listeners": {"web", "alt"},
		"routes":    {"-", "d", "-"},
		"upstreams": {"api", "web", "db"},
		"services":  {"tcp"},
	}
	if got := names(t, m.Data); !reflect.DeepEqual(got, want) {
		t.Fatalf("merged: %v, want %v\n%s", got, want, m.Data)
	}
	if !strings.Contains(string(m.Data), `"127.0.0.1:3"`) || strings.Contains(string(m.Data), `"127.0.0.1:2"`) || strings.Contains(string(m.Data), `"127.0.0.1:5"`) {
		t.Fatalf("the wrong upstreams won:\n%s", m.Data)
	}
	wantSources := []Source{
		{"/listeners/0", "file", ""}, {"/listeners/1", "consul", "/listeners/1"},
		{"/routes/0", "file", ""}, {"/routes/1", "docker", "/routes/0"}, {"/routes/2", "docker", "/routes/1"},
		{"/upstreams/0", "file", ""}, {"/upstreams/1", "docker", "/upstreams/1"}, {"/upstreams/2", "consul", "/upstreams/1"},
		{"/limits", "file", ""},
		{"/services/0", "docker", "/services/0"},
	}
	if !reflect.DeepEqual(m.Sources, wantSources) {
		t.Fatalf("sources:\n%v\nwant\n%v", m.Sources, wantSources)
	}
	wantShadowed := []Shadowed{
		{"consul", "/listeners/0", "web", "file"},
		{"consul", "/routes/0", "d", "docker"},
		{"docker", "/upstreams/0", "api", "file"},
		{"consul", "/upstreams/0", "web", "docker"},
	}
	if !reflect.DeepEqual(m.Shadowed, wantShadowed) {
		t.Fatalf("shadowed:\n%v\nwant\n%v", m.Shadowed, wantShadowed)
	}
	// The base config's text is kept as written.
	if !strings.HasPrefix(string(m.Data), base[:strings.Index(base, "}],")]) {
		t.Fatalf("the base config was not kept as written:\n%s", m.Data)
	}
}

func TestMergeIntoEmpty(t *testing.T) {
	part := mustParse(t, "p", `{"routes": [{"handler": {"module": "respond"}}], "listeners": [{"name": "l", "address": "127.0.0.1:0"}]}`)
	for _, base := range []string{`{}`, ` { } `, `{"routes": []}`, `{"routes": [ ], "listeners": []}`, `{"limits": {}}`} {
		m, err := Merge([]byte(base), "file", []*Part{part})
		if err != nil {
			t.Fatalf("%s: %v", base, err)
		}
		want := map[string][]string{"listeners": {"l"}, "routes": {"-"}}
		if got := names(t, m.Data); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: merged %v, want %v\n%s", base, got, want, m.Data)
		}
		if _, err := config.Load(m.Data); err != nil {
			t.Fatalf("%s: the merged config does not load: %v\n%s", base, err, m.Data)
		}
	}
	// Nothing to add leaves the base config itself.
	base := []byte(`{"routes": [{"handler": {"module": "respond"}}]}`)
	m, err := Merge(base, "file", []*Part{mustParse(t, "p", ``)})
	if err != nil || &m.Data[0] != &base[0] {
		t.Fatalf("an empty part: %v, %s", err, m.Data)
	}
}

func TestParseRefused(t *testing.T) {
	for _, tc := range []struct{ data, want string }{
		{`[]`, "must be a JSON object"},
		{`{"limits": {}}`, "limits: a provider may provide only"},
		{`{"routes": {}}`, "routes: must be an array"},
		{`{"routes": [1]}`, "routes[0]: must be an object"},
		{`{"routes": [{"name": 1}]}`, "routes[0].name: must be a string"},
		{`{"routes": [], "routes": []}`, "routes: is given twice"},
		{`{"routes": []} {}`, "has more after the top-level object"},
		{`{"routes": [}`, "invalid character"},
		{"{\n  \"routes\": [\n    1]}", "3:5: routes[0]: must be an object"},
	} {
		_, err := Parse("p", []byte(tc.data))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Parse(%s): %v, want %q", tc.data, err, tc.want)
		}
	}
	big := `{"routes": [` + strings.Repeat(" ", MaxPartBytes) + `]}`
	if _, err := Parse("p", []byte(big)); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("a part past MaxPartBytes: %v", err)
	}
	if _, err := Merge([]byte(`{"routes": null}`), "file", nil); err == nil || !strings.Contains(err.Error(), "routes: must be an array") {
		t.Errorf("a base config whose routes are null: %v", err)
	}
}

// TestErrors checks that errors in a merged config are reported where they
// were written.
func TestErrors(t *testing.T) {
	base := `{
  "routes": [{"handler": {"module": "respond"}}],
  "upstreams": [
    {"name": "u", "addresses": ["127.0.0.1:1"], "bogus": 1}
  ]
}`
	part := `{"routes": [
  {"name": "ok", "handler": {"module": "respond"}},
  {"name": "bad", "hanlder": {"module": "respond"}}
]}`
	m, err := Merge([]byte(base), "file", []*Part{mustParse(t, "docker", part)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = config.Load(m.Data)
	if err == nil {
		t.Fatal("the merged config loaded")
	}
	es := config.AsErrors(m.Errors(err))
	var inBase, inPart *config.Error
	for _, e := range es {
		switch {
		case strings.HasPrefix(e.Path, "upstreams[0].bogus"):
			inBase = e
		case strings.HasPrefix(e.Path, "routes[1].hanlder"):
			inPart = e
		}
	}
	if inBase == nil || inBase.Line != 4 || inBase.Col != 49 || strings.Contains(inBase.Msg, "provided by") {
		t.Errorf("the error in the base config: %v (all: %v)", inBase, es)
	}
	if inPart == nil || inPart.Line != 3 || inPart.Col != 19 || !strings.HasSuffix(inPart.Msg, "(provided by docker)") {
		t.Errorf("the error in the part: %v (all: %v)", inPart, es)
	}
	// An error that is not config's is left as it is.
	other := errors.New("other")
	if m.Errors(other) != other {
		t.Error("an error not config's was changed")
	}
}

func TestPosition(t *testing.T) {
	data := []byte("ab\néc\nd")
	for off := 0; off <= len(data); off++ {
		if off == 4 {
			continue // inside é, which starts at 3
		}
		line, col := position(data, off)
		if back := offset(data, line, col); back != off {
			t.Errorf("offset %d: %d:%d, back to %d", off, line, col, back)
		}
	}
	if line, col := position(data, 5); line != 2 || col != 2 {
		t.Errorf("after é: %d:%d", line, col)
	}
}

// fake is a provider whose parts a test sends.
type fake struct {
	name  string
	parts chan []byte
}

func (f *fake) Name() string { return f.name }

func (f *fake) Run(ctx context.Context, update func([]byte)) {
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-f.parts:
			update(p)
		}
	}
}

// TestRun checks that parts are applied one at a time, that a provider's
// latest part replaces one still waiting, and that Run returns when its
// context ends.
func TestRun(t *testing.T) {
	a := &fake{"a", make(chan []byte)}
	b := &fake{"b", make(chan []byte)}
	var (
		mu      sync.Mutex
		applied = map[string][]string{}
		busy    atomic.Int32
		gate    = make(chan struct{})
		entered = make(chan struct{})
	)
	apply := func(name string, data []byte) error {
		if busy.Add(1) != 1 {
			t.Error("two parts applied at once")
		}
		defer busy.Add(-1)
		if string(data) == "first" {
			close(entered)
			<-gate // hold the applier while more parts arrive
		}
		mu.Lock()
		applied[name] = append(applied[name], string(data))
		mu.Unlock()
		if string(data) == "fail" {
			return fmt.Errorf("refused")
		}
		return nil
	}
	var reported atomic.Value
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, []Provider{a, b}, apply, func(name string, err error) { reported.Store(name + ": " + err.Error()) })
	}()
	a.parts <- []byte("first")
	<-entered
	for i := range 100 {
		a.parts <- fmt.Appendf(nil, "a%d", i)
		b.parts <- fmt.Appendf(nil, "b%d", i)
	}
	b.parts <- []byte("fail")
	close(gate)
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		la, lb := applied["a"], applied["b"]
		ok := len(la) > 0 && la[len(la)-1] == "a99" && len(lb) > 0 && lb[len(lb)-1] == "fail"
		mu.Unlock()
		if ok {
			if len(la) > 3 || len(lb) > 2 {
				t.Errorf("parts waiting were not replaced: a %v, b %v", la, lb)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the latest parts were not applied: a %v, b %v", la, lb)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got, _ := reported.Load().(string); got != "b: refused" {
		t.Errorf("reported %q", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
}
