package compress

import (
	stdhttp "net/http"
	"slices"
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

// TestCompressLevels checks the levels' names, and the levels by number,
// which take an encoding's place in them.
func TestCompressLevels(t *testing.T) {
	for params, want := range map[string][3]int{
		`{}`:                   {1, 1, 1},
		`{"level": "fastest"}`: {1, 1, 1},
		`{"level": "default"}`: {5, 4, 3},
		`{"level": "better"}`:  {7, 6, 6},
		`{"gzip_level": 9}`:    {9, 1, 1},
		`{"level": "better", "brotli_level": 11, "zstd_level": 22}`: {7, 11, 22},
	} {
		cm := loadCompress(t, params)
		if got := [3]int{cm.gzipLevel, cm.brotliLevel, cm.zstdLevel}; got != want || cm.InFlight() != 0 {
			t.Errorf("%s: levels %v, want %v, %d in flight", params, got, want, cm.InFlight())
		}
	}
	if cm := loadCompress(t, `{}`); cm.MinBytes != 1400 || !slices.Equal(cm.encodings, []string{Zstd, Gzip, Brotli}) {
		t.Errorf("defaults: min_bytes %d, encodings %v", cm.MinBytes, cm.encodings)
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
