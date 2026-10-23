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
