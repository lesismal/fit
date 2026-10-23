package main

import (
	"strings"
	"testing"
)

func TestShare(t *testing.T) {
	for _, c := range []struct {
		v, best float64
		want    string
	}{
		{3000, 3000, "3,000 100%"},
		{2999, 3000, "2,999  99%"},
		{10, 3000, "10   0%"},
		{5, 0, "5"},
	} {
		if got := share(c.v, c.best, 0); got != c.want {
			t.Errorf("share(%v, %v) = %q, want %q", c.v, c.best, got, c.want)
		}
	}
	if got := shareLow(1204, 545); got != "1,204  45%" {
		t.Errorf("shareLow = %q", got)
	}
}

func TestGroup(t *testing.T) {
	for v, want := range map[float64]string{0: "0", 999: "999", 1000: "1,000", 3816927: "3,816,927", -1234: "-1,234"} {
		if got := group(v, 0); got != want {
			t.Errorf("group(%v) = %q, want %q", v, got, want)
		}
	}
	if got := group(4175.5, 1); got != "4,175.5" {
		t.Errorf("group(4175.5, 1) = %q", got)
	}
}

// A table ranks its rows by throughput, then CPU efficiency, then memory
// efficiency, says which servers did not run, and compares with the earlier
// run.
func TestRender(t *testing.T) {
	cur := &run{Results: []result{
		{Server: "a", Scenario: "fixed", Protocol: "h2c", RequestsPerSec: 100, CPUCores: 2, AnonBytes: 1 << 20},
		{Server: "b", Scenario: "fixed", Protocol: "h2c", RequestsPerSec: 200, CPUCores: 4, AnonBytes: 4 << 20},
		{Server: "c", Scenario: "fixed", Protocol: "h2c", RequestsPerSec: 100, CPUCores: 1, AnonBytes: 1 << 20},
		{Server: "d", Scenario: "fixed", Protocol: "h2c", Skipped: "d cannot serve it"},
	}}
	old := &run{Results: []result{{Server: "b", Scenario: "fixed", Protocol: "h2c", RequestsPerSec: 160}}}
	var out strings.Builder
	render(&out, cur, old, "M1", 3, false, "")
	text := out.String()
	b, c, a := strings.Index(text, "| b "), strings.Index(text, "| c "), strings.Index(text, "| a ")
	if !(b > 0 && b < c && c < a) {
		t.Fatalf("rows not ranked b, c, a:\n%s", text)
	}
	for _, want := range []string{"### fixed, h2c", "TPS vs M1", "+25%", "200 100%", "100 100%", "Not run: d, which cannot serve it."} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

// Throughput counts successful responses only, and a run with no result
// says why.
func TestSuccessesAndFailures(t *testing.T) {
	r := result{RequestsPerSec: 1000, Requests: 30000, Errors: 22500, CPUCores: 2}
	if !near(r.tps(), 250) || !near(r.cpuEER(), 125) {
		t.Errorf("tps %v, cpuEER %v; want 250 and 125", r.tps(), r.cpuEER())
	}
	if got := r.errorsCell(); got != "22,500 (75%)" {
		t.Errorf("errorsCell = %q", got)
	}
	if got := (result{}).errorsCell(); got != "no responses" {
		t.Errorf("errorsCell of an empty run = %q", got)
	}
	for err, want := range map[string]string{
		"docker exec fitbench-loadgen h2load: exit status 137: starting benchmark...": "hung",
		"fitbench-server did not answer at http://127.0.0.1:8080/fixed within 30s":    "did not start",
		"check http://127.0.0.1:8080/fixed: got 502":                                  "error",
	} {
		if got := (result{Error: err}).failure(); got != want {
			t.Errorf("failure(%q) = %q, want %q", err, got, want)
		}
	}
}

func near(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }

// A sweep's table has a column for each run, headed by its connections, and
// ranks its rows by the most a server answered at any of them.
func TestRenderSweep(t *testing.T) {
	mk := func(conns, h2conns int, rows ...result) *run {
		r := &run{Results: rows}
		r.Params.Connections, r.Params.H2Connections, r.Params.H2Streams = conns, h2conns, 32
		return r
	}
	runs := []*run{
		mk(16, 8,
			result{Server: "a", Scenario: "proxy", Protocol: "h2c", RequestsPerSec: 100000, CPUCores: 1},
			result{Server: "b", Scenario: "proxy", Protocol: "h2c", RequestsPerSec: 150000, CPUCores: 1}),
		mk(64, 16,
			result{Server: "a", Scenario: "proxy", Protocol: "h2c", RequestsPerSec: 200000, CPUCores: 2},
			result{Server: "b", Scenario: "proxy", Protocol: "h2c", Error: "h2load: exit status 137"}),
		mk(256, 64,
			result{Server: "a", Scenario: "proxy", Protocol: "h2c"},
			result{Server: "b", Scenario: "proxy", Protocol: "h2c", Skipped: "b cannot serve it"}),
	}
	var out strings.Builder
	renderSweep(&out, runs, 3)
	text := out.String()
	for _, want := range []string{"### proxy, h2c", "8×32", "16×32", "64×32", "Peak TPS [↓1]", "200k 100%", "hung", "—"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if a, b := strings.Index(text, "| a "), strings.Index(text, "| b "); !(a > 0 && a < b) {
		t.Errorf("rows not ranked a, b:\n%s", text)
	}
}
