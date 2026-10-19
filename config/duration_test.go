package config

import (
	"strings"
	"testing"
	"time"
)

func TestDuration(t *testing.T) {
	type params struct {
		Timeout  Duration   `json:"timeout"`
		Timeouts []Duration `json:"timeouts,omitempty"`
		Optional *Duration  `json:"optional,omitempty"`
	}
	v, err := ParseValue([]byte(`{"timeout": "1m30s", "timeouts": ["0", "250ms", "1.5h"], "optional": "2s"}`), "p")
	if err != nil {
		t.Fatal(err)
	}
	var p params
	if err := v.Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.Timeout.Std() != 90*time.Second || len(p.Timeouts) != 3 || p.Timeouts[0] != 0 ||
		p.Timeouts[1].Std() != 250*time.Millisecond || p.Timeouts[2].Std() != 90*time.Minute ||
		*p.Optional != Duration(2*time.Second) || p.Timeout.String() != "1m30s" {
		t.Fatalf("decoded %+v", p)
	}

	v, err = ParseValue([]byte(`{"timeout": 10, "timeouts": ["10", "-1s", "1d"]}`), "p")
	if err != nil {
		t.Fatal(err)
	}
	err = v.Decode(&p)
	want := strings.Join([]string{
		`1:13: p.timeout: expected a string, got a number`,
		`1:30: p.timeouts[0]: "10" is not a duration, such as "10s" or "1m30s"`,
		`1:36: p.timeouts[1]: "-1s" is negative`,
		`1:43: p.timeouts[2]: "1d" is not a duration, such as "10s" or "1m30s"`,
	}, "\n")
	if err == nil || err.Error() != want {
		t.Fatalf("errors:\n%v\nwant:\n%s", err, want)
	}
}
