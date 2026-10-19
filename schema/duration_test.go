package schema

import (
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
