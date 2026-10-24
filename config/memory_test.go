package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lesismal/fit/internal/sysmem"
)

func TestMemory(t *testing.T) {
	load := func(v string) (*Config, error) {
		return Load([]byte(`{"listeners": [{"name": "a", "address": ":1"}], "limits": {"max_pending_bytes": ` + v + `}}`))
	}
	total := sysmem.Total()
	for v, want := range map[string]int64{
		`1048576`: 1 << 20,
		`"25%"`:   int64(total / 4),
		`"12.5%"`: int64(float64(total) * 0.125),
		`"100%"`:  int64(total),
	} {
		c, err := load(v)
		if err != nil {
			t.Fatalf("%s: %v", v, err)
		}
		if total == 0 && strings.HasSuffix(v, `%"`) {
			want = FallbackMaxPendingBytes
		}
		if got := c.Limits.PendingBytes(); got != want {
			t.Errorf("%s: %d bytes, want %d", v, got, want)
		}
		out, err := json.Marshal(c.Limits.MaxPendingBytes)
		if err != nil || string(out) != v {
			t.Errorf("%s marshalled as %s %v", v, out, err)
		}
	}
	for v, msg := range map[string]string{
		`"0%"`:     "not more than 0% and at most 100%",
		`"0.001%"`: "not more than 0% and at most 100%",
		`"101%"`:   "not more than 0% and at most 100%",
		`"NaN%"`:   "not more than 0% and at most 100%",
		`"50"`:     `"50" is not a share of memory`,
		`" 5%"`:    "is not a share of memory",
		`"half"`:   "is not a share of memory",
		`-1`:       "is not a number of bytes, 0 or more",
		`1.5`:      "is not a number of bytes, 0 or more",
		`true`:     "expected a number of bytes or a share of memory",
	} {
		if _, err := load(v); err == nil || !strings.Contains(err.Error(), "limits.max_pending_bytes: ") || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: %v, want %q", v, err, msg)
		}
	}
	if p, ok := MemoryPercent(50).Percent(); !ok || p != 50 || MemoryPercent(50).String() != "50%" || Memory(7).String() != "7" {
		t.Errorf("MemoryPercent(50) = %v %v %s", p, ok, MemoryPercent(50))
	}
	if got := MemoryPercent(0.01).Bytes(9); total > 0 && got < 1 || total == 0 && got != 9 {
		t.Errorf("0.01%% of %d = %d", total, got)
	}
}
