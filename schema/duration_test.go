package schema

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/lesismal/fit/config"
)

// TestDurationPattern checks the schema's pattern for durations against what
// the decoder takes.
func TestDurationPattern(t *testing.T) {
	pattern := regexp.MustCompile(durationPattern)
	for _, s := range []string{"0", "10s", "1m30s", "1.5h", ".5s", "250ms", "3us", "3µs", "+5m", "1h2m3s4ms",
		"10", "00", "-1s", "1d", "", "s", "1.s5", " 1s"} {
		v, err := config.ParseValue([]byte(`"`+s+`"`), "d")
		if err != nil {
			t.Fatal(err)
		}
		var d config.Duration
		decoded := v.Decode(&d) == nil
		if pattern.MatchString(s) != decoded {
			t.Errorf("%q: the pattern matches it: %v; the decoder takes it: %v", s, !decoded, decoded)
		}
	}
}

// TestDefaults checks that the defaults the code applies are in the schema.
func TestDefaults(t *testing.T) {
	out, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Defs map[string]struct {
			Properties map[string]struct {
				Default any `json:"default"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(out, &s); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		def, field string
		want       any
	}{
		{"config.Limits", "max_pending_bytes", "50%"},
		{"config.Limits", "max_header_bytes", float64(config.DefaultMaxHeaderBytes)},
		{"config.Limits", "max_body_bytes", float64(config.DefaultMaxBodyBytes)},
		{"config.Limits", "write_stall_timeout", "30s"},
		{"config.Upstream", "max_conns", float64(256)},
		{"config.Upstream", "max_pending", float64(1024)},
		{"config.Upstream", "response_timeout", "1h"},
		{"http.handlers.static", "browse_max_entries", float64(10000)},
	} {
		got := s.Defs[tt.def].Properties[tt.field].Default
		if got != tt.want {
			t.Errorf("%s.%s: default %v, want %v", tt.def, tt.field, got, tt.want)
		}
	}
}
