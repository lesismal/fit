package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRun writes r as a fitbench report in dir, and returns its path.
func writeRun(t *testing.T, dir, name string, r *run) string {
	t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// idleRow is one of a run's idle connection results.
func idleRow(server, kind string, bytes float64, skipped, failed string) struct {
	Server       string  `json:"server"`
	Kind         string  `json:"kind"`
	Connections  int     `json:"connections"`
	Skipped      string  `json:"skipped"`
	Error        string  `json:"error"`
	BytesPerConn float64 `json:"bytes_per_conn"`
} {
	return struct {
		Server       string  `json:"server"`
		Kind         string  `json:"kind"`
		Connections  int     `json:"connections"`
		Skipped      string  `json:"skipped"`
		Error        string  `json:"error"`
		BytesPerConn float64 `json:"bytes_per_conn"`
	}{server, kind, 10000, skipped, failed, bytes}
}

// coverRuns are a run and an earlier one with a row for every case a table
// renders: ties, failures, servers that do not run a scenario, values not
// measured, and idle connections measured, failed and skipped.
func coverRuns() (cur, old *run) {
	half := 0.5
	type lat = struct {
		P50  float64 `json:"p50"`
		P99  float64 `json:"p99"`
		P999 float64 `json:"p99.9"`
		Mean float64 `json:"mean"`
		Max  float64 `json:"max"`
	}
	cur = &run{Results: []result{
		{Server: "a", Scenario: "fixed", Protocol: "http/1.1", RequestsPerSec: 100, CPUCores: 1, AnonPeakBytes: 2 << 20,
			BytesPerInFlight: 2048, HitRatio: &half, Latency: &lat{P99: 1.5}},
		// Ties with a on every ranked value: listed in the servers' order.
		{Server: "b", Scenario: "fixed", Protocol: "http/1.1", RequestsPerSec: 100, CPUCores: 1, AnonPeakBytes: 2 << 20},
		{Server: "c", Scenario: "fixed", Protocol: "http/1.1", Error: "boom"},
		// No CPU or memory read, and only h2load's latency.
		{Server: "d", Scenario: "fixed", Protocol: "http/1.1", RequestsPerSec: 50, Latency: &lat{Mean: 2, Max: 9}},
		// A protocol without a display name.
		{Server: "a", Scenario: "fixed", Protocol: "quic9", RequestsPerSec: 10, AnonBytes: 1 << 20},
		// A table no server ran.
		{Server: "a", Scenario: "static", Protocol: "h2c", Skipped: "a cannot serve it"},
	}}
	cur.Idle = append(cur.Idle,
		idleRow("a", "http1", 1000, "", ""), idleRow("a", "tls", 3000, "", ""),
		idleRow("b", "http1", 500, "", ""), idleRow("b", "tls", 0, "", "refused"),
		idleRow("e", "http1", 0, "e cannot serve it", ""), idleRow("e", "tls", 2000, "", ""),
		idleRow("g", "http1", 1000, "", ""), idleRow("g", "tls", 3000, "", ""),
		// After b in the servers' order, but more than it.
		idleRow("d", "http1", 5000, "", ""),
	)
	old = &run{Results: []result{{Server: "a", Scenario: "fixed", Protocol: "http/1.1", RequestsPerSec: 100}}}
	old.Idle = append(old.Idle, idleRow("a", "http1", 900, "", ""), idleRow("b", "http1", 0, "", ""))
	return cur, old
}

func TestRenderEveryCase(t *testing.T) {
	cur, old := coverRuns()
	var out strings.Builder
	render(&out, cur, old, "M1", 2, true, "Idle")
	text := out.String()
	for _, want := range []string{
		"## fixed, HTTP/1.1", "| Server | TPS [↓1]", "p99", "Peak memory", "KiB/in-flight", "Hits", "TPS vs M1",
		"1.50 ms", "2.00 / 9 ms", "50.00%", " 0% |", "—", "| c ", "error",
		"## fixed, quic9", "Mean / max",
		"## Idle", "http1 [↑1]", "tls [↑2]", "(900)", "In brackets, M1's.", "With 10,000 connections",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "static") {
		t.Errorf("a table for a scenario no server ran:\n%s", text)
	}
	// Ranked: a before b on a tie, then d; c's error after.
	a, b, d, c := strings.Index(text, "| a "), strings.Index(text, "| b "), strings.Index(text, "| d "), strings.Index(text, "| c ")
	if !(a < b && b < d && d < c) {
		t.Errorf("rows not ranked a, b, d, c:\n%s", text)
	}
	// The idle table: b lowest, a and g tied, e last for its http1 not
	// measured; b's tls failed, e's http1 not run.
	idle := text[strings.Index(text, "## Idle"):]
	ib, ia, ig, id, ie := strings.Index(idle, "| b "), strings.Index(idle, "| a "), strings.Index(idle, "| g "),
		strings.Index(idle, "| d "), strings.Index(idle, "| e ")
	if !(ib < ia && ia < ig && ig < id && id < ie) {
		t.Errorf("idle rows not ranked b, a, g, d, e:\n%s", idle)
	}
	if !strings.Contains(idle, "error") || !strings.Contains(idle, "—") {
		t.Errorf("no failed or unmeasured cell:\n%s", idle)
	}

	// Without an earlier run, no comparison.
	out.Reset()
	render(&out, cur, nil, "M1", 3, true, "Idle")
	if text := out.String(); strings.Contains(text, "TPS vs") || strings.Contains(text, "In brackets") || strings.Contains(text, "(900)") {
		t.Errorf("compared with nothing:\n%s", text)
	}
}

func TestShareLowUnmeasured(t *testing.T) {
	if got := shareLow(0, 545); got != "0" {
		t.Errorf("shareLow(0) = %q", got)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if _, err := load(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("loaded a missing file")
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := load(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Errorf("got %v, want an error naming the file", err)
	}
}

// TestMainCommand runs the command itself on two runs' files, rendering the
// idle connections alone.
func TestMainCommand(t *testing.T) {
	dir := t.TempDir()
	cur, old := coverRuns()
	in, prev := writeRun(t, dir, "cur.json", cur), writeRun(t, dir, "old.json", old)
	stdout, err := os.Create(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	oldOut, oldArgs, oldFlags := os.Stdout, os.Args, flag.CommandLine
	defer func() { os.Stdout, os.Args, flag.CommandLine = oldOut, oldArgs, oldFlags }()
	os.Stdout = stdout
	os.Args = []string{"benchreport", "-in", in, "-prev", prev, "-prev-name", "v1", "-idle-only", "-idle-heading", "Idle"}
	flag.CommandLine = flag.NewFlagSet("benchreport", flag.ContinueOnError)
	main()
	got, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.HasPrefix(text, "### Idle\n") || !strings.Contains(text, "In brackets, v1's.") || strings.Contains(text, "fixed") {
		t.Errorf("got:\n%s", text)
	}
}

// -sweep renders the runs it names as scaling tables, in the order given.
func TestMainSweep(t *testing.T) {
	dir := t.TempDir()
	cur, old := coverRuns()
	a, b := writeRun(t, dir, "a.json", old), writeRun(t, dir, "b.json", cur)
	stdout, err := os.Create(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	oldOut, oldArgs, oldFlags := os.Stdout, os.Args, flag.CommandLine
	defer func() { os.Stdout, os.Args, flag.CommandLine = oldOut, oldArgs, oldFlags }()
	os.Stdout = stdout
	os.Args = []string{"benchreport", "-sweep", a + "," + b, "-level", "4"}
	flag.CommandLine = flag.NewFlagSet("benchreport", flag.ContinueOnError)
	main()
	got, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	if text := string(got); !strings.HasPrefix(text, "#### ") || !strings.Contains(text, "Peak TPS [↓1]") {
		t.Errorf("got:\n%s", text)
	}
}

// A sweep leaves out a scenario no server ran, and names a protocol it has
// no name for as it is.
func TestRenderSweepEdges(t *testing.T) {
	r := &run{Results: []result{
		{Server: "a", Scenario: "fixed", Protocol: "h9", RequestsPerSec: 10, CPUCores: 1},
		{Server: "a", Scenario: "static", Protocol: "h9", Skipped: "a cannot serve it"},
	}}
	var out strings.Builder
	renderSweep(&out, []*run{r}, 3)
	text := out.String()
	if !strings.Contains(text, "### fixed, h9") || strings.Contains(text, "static") {
		t.Errorf("got:\n%s", text)
	}
}
