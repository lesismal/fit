package provider

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

func TestKinds(t *testing.T) {
	k := Kind(func(name string, _ *config.Provider, _ func(string, ...any)) (Provider, string) {
		return &fake{name: name}, "watching nothing"
	})
	RegisterKind("test_kind", k)
	got, ok := LookupKind("test_kind")
	if !ok {
		t.Fatal("the kind registered is not found")
	}
	if p, watching := got("p", nil, nil); p.Name() != "p" || watching != "watching nothing" {
		t.Errorf("the kind made %q, %q", p.Name(), watching)
	}
	if _, ok := LookupKind("no_such_kind"); ok {
		t.Error("a kind not registered is found")
	}
	if kinds := RegisteredKinds(); !slices.Contains(kinds, "test_kind") || !slices.IsSorted(kinds) {
		t.Errorf("RegisteredKinds: %v", kinds)
	}
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "kind test_kind is registered twice") {
			t.Errorf("registering twice: %v", r)
		}
	}()
	RegisterKind("test_kind", k)
}

// stepper is a provider that sends two parts, the second only once the
// first is being applied.
type stepper struct {
	applying <-chan struct{}
	sent     chan<- struct{}
}

func (s *stepper) Name() string { return "s" }

func (s *stepper) Run(ctx context.Context, update func([]byte)) {
	update([]byte("1"))
	<-s.applying
	update([]byte("2"))
	close(s.sent)
	<-ctx.Done()
}

// TestRunCanceledWhileApplying checks that a part that waits is not
// applied once the context ends, and that report may be nil.
func TestRunCanceledWhileApplying(t *testing.T) {
	applying, sent := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	var applied []string
	apply := func(_ string, data []byte) error {
		applied = append(applied, string(data))
		if string(data) == "1" {
			close(applying)
			<-sent // the second part waits
			cancel()
			return errNotApplied
		}
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, []Provider{&stepper{applying, sent}}, apply, nil)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	if !slices.Equal(applied, []string{"1"}) {
		t.Errorf("applied %q", applied)
	}
}

var errNotApplied = config.Errors{{Msg: "not applied"}}

func TestParseRefusedMore(t *testing.T) {
	// What the errors say is encoding/json's, and differs between its
	// versions; where they are is Parse's.
	for _, tc := range []struct{ data, want string }{
		{`{1}`, "1:"},
		{`{"routes": }`, "1:"},
		{`{"routes": []`, "1:"},
		{`{"routes": []]`, "1:"},
	} {
		_, err := Parse("p", []byte(tc.data))
		if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
			t.Errorf("Parse(%s): %v, want %q", tc.data, err, tc.want)
		}
	}
	// A base config's member not of Kinds that is not JSON.
	if _, err := Merge([]byte(`{"limits": [}`), "file", nil); err == nil || !strings.HasPrefix(err.Error(), "1:") {
		t.Errorf("a base config's member that is not JSON: %v", err)
	}
}

func TestErrorsOne(t *testing.T) {
	base := "{\n  \"limits\": {\"bogus\": 1}\n}"
	m, err := Merge([]byte(base), "file", []*Part{mustParse(t, "p", `{"routes": [{"handler": {"module": "respond"}}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	// One *config.Error, not in an element, in the base config.
	es := config.AsErrors(m.Errors(&config.Error{Path: "limits.bogus", Line: 2, Col: 14, Msg: "unknown"}))
	if len(es) != 1 || es[0].Path != "limits.bogus" || es[0].Line != 2 || es[0].Col != 14 || es[0].Msg != "unknown" {
		t.Errorf("errors: %v", es)
	}
}

func TestOffsetPastEnd(t *testing.T) {
	if off := offset([]byte("a\nb"), 5, 1); off != 3 {
		t.Errorf("a line past the end: %d", off)
	}
}
