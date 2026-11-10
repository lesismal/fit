package main

import (
	"flag"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type lat = struct {
	P50  float64 `json:"p50"`
	P99  float64 `json:"p99"`
	P999 float64 `json:"p99.9"`
	Mean float64 `json:"mean"`
	Max  float64 `json:"max"`
}

// A table's best server on every share scores 100; one at half of each scores
// 50; errors count twice, in the throughput that leaves them out and in the
// score they scale down; a failed or skipped run scores 0.
func TestScores(t *testing.T) {
	rows := []result{
		{Server: "a", RequestsPerSec: 200, CPUCores: 2, AnonBytes: 1 << 20, Latency: &lat{P99: 1}},
		{Server: "b", RequestsPerSec: 100, CPUCores: 2, AnonBytes: 1 << 20, Latency: &lat{P99: 2}},
		{Server: "c", RequestsPerSec: 200, CPUCores: 2, AnonBytes: 1 << 20, Latency: &lat{P99: 1}, Requests: 100, Errors: 25},
		{Server: "d", Error: "exit status 137"},
		{Server: "e", Skipped: "cannot serve it"},
	}
	sc := scores(rows, defaultMetrics)
	for server, want := range map[string]float64{"a": 100, "b": 50, "c": 58.125, "d": 0, "e": 0} {
		if math.Abs(sc[server]-want) > 1e-9 {
			t.Errorf("%s scores %v, want %v", server, sc[server], want)
		}
	}
	// Nothing measured for anyone neither lifts nor sinks a server: shares
	// of throughput alone decide.
	sc = scores([]result{{Server: "a", RequestsPerSec: 100}, {Server: "b", RequestsPerSec: 50}}, rateMetrics)
	if math.Abs(sc["a"]-100) > 1e-9 || math.Abs(sc["b"]-90) > 1e-9 {
		t.Errorf("unmeasured shares: %v", sc)
	}
	// A server without a latency where others have one gets none of it.
	sc = scores([]result{{Server: "a", RequestsPerSec: 100, Latency: &lat{Mean: 2}}, {Server: "b", RequestsPerSec: 100}}, rateMetrics)
	if math.Abs(sc["b"]-50) > 1e-9 || math.Abs(sc["a"]-100) > 1e-9 {
		t.Errorf("missing latency: %v", sc)
	}
	// Nor one without its CPU where others have theirs.
	sc = scores([]result{{Server: "a", RequestsPerSec: 100, CPUCores: 1}, {Server: "b", RequestsPerSec: 100}}, defaultMetrics)
	if math.Abs(sc["b"]-75) > 1e-9 {
		t.Errorf("missing CPU: %v", sc)
	}
}

func TestLatencyOf(t *testing.T) {
	if latencyOf(result{}) != 0 || latencyOf(result{Latency: &lat{P99: 3, Mean: 1}}) != 3 || latencyOf(result{Latency: &lat{Mean: 1}}) != 1 {
		t.Error("latencyOf")
	}
}

func TestCompareNames(t *testing.T) {
	for _, c := range []struct {
		a, b string
		less bool
	}{
		{"w0-fixed-500.json", "w0-fixed-2000.json", true},
		{"w0-fixed-2000", "w0-fixed-500", false},
		{"w0-fixed-500", "w1-api-100", true},
		{"w0-fixed-x", "w0-fixed-1", false},
		{"w0-fixed-1", "w0-fixed-x", true},
		{"plain", "w0", true},
		{"s9-mssql-kafka.json", "s10-mqtt-influxdb.json", true},
		{"s10-mqtt-influxdb", "s2-mysql-es", false},
		{"s1-files-128b-1f", "s10-mqtt-influxdb", true},
		{"s10-mqtt-influxdb", "w0-fixed-1", true},
	} {
		if got := compareNames(c.a, c.b) < 0; got != c.less {
			t.Errorf("compareNames(%q, %q) < 0 = %v", c.a, c.b, got)
		}
	}
}

// planDir writes a small plan: two levels of W0, one of W6 at a fixed rate, a
// file of no workload, and two servers W0 does not run, which tie.
func planDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	w0 := func(rps float64) *run {
		return &run{Results: []result{
			{Server: "nginx", Scenario: "fixed", Protocol: "h2c", RequestsPerSec: 100, CPUCores: 1, AnonBytes: 1 << 20},
			{Server: "fit", Scenario: "fixed", Protocol: "h2c", RequestsPerSec: rps, CPUCores: 1, AnonBytes: 1 << 20},
			{Server: "fit", Scenario: "fixed", Protocol: "x-proto", RequestsPerSec: 100, CPUCores: 1, AnonBytes: 1 << 20},
			{Server: "nginx", Scenario: "fixed", Protocol: "x-proto", Error: "boom"},
		}}
	}
	writeRun(t, dir, "w0-fixed-100.json", w0(200))
	writeRun(t, dir, "w0-fixed-2000.json", w0(100))
	writeRun(t, dir, "w6-rate-1000.json", &run{Results: []result{
		{Server: "fit", Scenario: "api", Protocol: "h2", Rate: 1000, RequestsPerSec: 1000, CPUCores: 1, AnonBytes: 1 << 20, Latency: &lat{P99: 1}},
		{Server: "nginx", Scenario: "api", Protocol: "h2", Rate: 1000, RequestsPerSec: 1000, CPUCores: 1, AnonBytes: 1 << 20, Latency: &lat{P99: 2}},
		{Server: "traefik", Scenario: "api", Protocol: "h2", Rate: 1000, Skipped: "cannot serve it"},
		{Server: "envoy", Scenario: "api", Protocol: "h2", Rate: 1000, Skipped: "cannot serve it"},
	}})
	writeRun(t, dir, "w9-other-1.json", &run{Results: []result{{Server: "z", Scenario: "s", Protocol: "h2"}}})
	return dir
}

// The plan's items share their workload's weight; the ranking orders the
// servers by their weighted mean, gives each workload's, and the coverage.
func TestRenderScores(t *testing.T) {
	p, err := loadPlan(planDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.runs) != 3 || len(p.weights) != 5 {
		t.Fatalf("%d runs, %d items", len(p.runs), len(p.weights))
	}
	if w := p.weights[item{"w0-fixed-100", "fixed", "h2c"}]; math.Abs(w-0.15/4) > 1e-12 {
		t.Errorf("W0 item weight %v", w)
	}
	if w := p.weights[item{"w6-rate-1000", "api", "h2"}]; math.Abs(w-0.15) > 1e-12 {
		t.Errorf("W6 item weight %v", w)
	}
	var out strings.Builder
	renderScores(&out, p, 2)
	text := out.String()
	for _, want := range []string{"## Overall ranking", "| 1    | fit ", "| 2    | nginx ", "| 3    | traefik |   0.0 |", "| 4    | envoy   |   0.0 |",
		"## How the items are scored", "W6, latency at a fixed rate", "## Every item", "w0-fixed-100: fixed, h2c", "x-proto",
		"**100.0**", "50%"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "| z ") || strings.Contains(text, "W1, the API gateway") {
		t.Errorf("a file of no workload, or a workload with no items, shown:\n%s", text)
	}
	// Traefik runs only in W6.
	if !strings.Contains(text, "|       — |  0.0 |") && !strings.Contains(text, "50%") {
		t.Errorf("coverage:\n%s", text)
	}
	if _, err := loadPlan(t.TempDir()); err == nil {
		t.Error("an empty folder is a plan")
	}
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "w0-fixed-1.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPlan(bad); err == nil {
		t.Error("a broken file read")
	}
	if _, err := loadPlan("["); err == nil {
		t.Error("a bad pattern read")
	}
	if renderScoresFile(&out, t.TempDir(), 2) == nil {
		t.Error("renderScoresFile of an empty folder")
	}
}

// runMain runs the command with args and returns what it wrote.
func runMain(t *testing.T, args ...string) string {
	t.Helper()
	stdout, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	oldOut, oldArgs, oldFlags, oldScoring := os.Stdout, os.Args, flag.CommandLine, scoring
	defer func() { os.Stdout, os.Args, flag.CommandLine, scoring = oldOut, oldArgs, oldFlags, oldScoring }()
	os.Stdout = stdout
	os.Args = append([]string{"benchreport"}, args...)
	flag.CommandLine = flag.NewFlagSet("benchreport", flag.ContinueOnError)
	main()
	got, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

// -scores renders the ranking; -plan gives each table its weight and each
// row its score, a failed run's 0 included.
func TestMainScores(t *testing.T) {
	dir := planDir(t)
	if text := runMain(t, "-scores", dir); !strings.HasPrefix(text, "### Overall ranking") {
		t.Errorf("-scores:\n%s", text)
	}
	text := runMain(t, "-plan", dir, "-in", filepath.Join(dir, "w0-fixed-100.json"))
	for _, want := range []string{"### fixed, h2c (weight 3.75%)", "| Score |", "| fit    | 100.0 |", "| nginx  |   0.0 |    error |"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	text = runMain(t, "-plan", dir, "-in", filepath.Join(dir, "w6-rate-1000.json"))
	if !strings.Contains(text, "It scores 0 for this item.") {
		t.Errorf("skipped server's score not said:\n%s", text)
	}
}
