package compress

import (
	stdhttp "net/http"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// loadCompress loads the compress middleware with params.
func loadCompress(t *testing.T, params string) *Compress {
	t.Helper()
	v, err := config.ParseValue([]byte(`{"module": "compress", "params": `+params+`}`), "middleware")
	if err != nil {
		t.Fatal(err)
	}
	var ref config.ModuleRef
	if err := v.Decode(&ref); err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(module.Middlewares, ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m.(*Compress)
}

// TestCompressLevels checks the levels' names.
func TestCompressLevels(t *testing.T) {
	for params, want := range map[string]int{`{"level": "fastest"}`: 0, `{}`: 1, `{"level": "better"}`: 2} {
		if cm := loadCompress(t, params); cm.level != want || cm.InFlight() != 0 {
			t.Errorf("%s: level %d, %d in flight", params, cm.level, cm.InFlight())
		}
	}
}

// TestEligibleAndVary checks responses that are not compressed, and a Vary
// that already covers Accept-Encoding.
func TestEligibleAndVary(t *testing.T) {
	cm := loadCompress(t, `{}`)
	text := func(k, v string) stdhttp.Header { return stdhttp.Header{"Content-Type": {"text/plain"}, k: {v}} }
	for _, tt := range []struct {
		status int
		h      stdhttp.Header
		want   bool
	}{
		{200, text("Content-Range", "bytes 0-1/2"), false},
		{200, text("Cache-Control", "public, No-Transform"), false},
		{200, text("X", "y"), true},
	} {
		if got := cm.eligible(tt.status, tt.h); got != tt.want {
			t.Errorf("%d %v: %v", tt.status, tt.h, got)
		}
	}
	for _, vary := range []string{"*", "Origin, accept-encoding"} {
		h := stdhttp.Header{"Vary": {vary}}
		addVary(h)
		if len(h["Vary"]) != 1 {
			t.Errorf("%q: %v", vary, h["Vary"])
		}
	}
	h := stdhttp.Header{"Vary": {"Origin"}}
	addVary(h)
	if len(h["Vary"]) != 2 {
		t.Errorf("Origin: %v", h["Vary"])
	}
}
