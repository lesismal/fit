package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// syncFile is a results file as bench/sync/syncbench merge writes it.
func syncFile(t *testing.T, dir, name, scenario string, results string) string {
	t.Helper()
	p := filepath.Join(dir, name+".json")
	data := `{"kind": "sync", "name": "` + name + `", "params": {"records": "100"}, "results": [` + results + `], "runs": []}`
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func syncPlanDir(t *testing.T) string {
	dir := t.TempDir()
	syncFile(t, dir, "s1-files-128b-1f", "files", `
		{"mover": "fit", "scenario": "files", "variant": "s1-files-128b-1f", "records": 100, "counted": 100,
		 "records_per_sec": 2000, "cpu_cores": 1, "rss_peak_bytes": 1048576, "startup_seconds": 0.1,
		 "lag_ms": {"rate": 10, "p50": 5, "p99": 10, "max": 12}, "rounds": 3, "spread": 0.02},
		{"mover": "filebeat", "scenario": "files", "variant": "s1-files-128b-1f", "records": 100, "counted": 100, "extra": 2,
		 "records_per_sec": 1000, "cpu_cores": 1, "rss_peak_bytes": 2097152, "startup_seconds": 0.2,
		 "lag_ms": {"rate": 10, "p50": 10, "p99": 20, "max": 30}, "rounds": 3, "spread": 0.1, "failures": ["once"]}`)
	syncFile(t, dir, "s5-mysql-batch", "mysql-batch", `
		{"mover": "fit", "scenario": "mysql-batch", "variant": "s5-mysql-batch", "records": 100, "counted": 100,
		 "records_per_sec": 500, "cpu_cores": 1, "rss_peak_bytes": 1048576, "rounds": 1},
		{"mover": "datax", "scenario": "mysql-batch", "variant": "s5-mysql-batch", "records": 100, "counted": 40,
		 "error": "the sink had 40 of 100 records", "rounds": 1}`)
	syncFile(t, dir, "s10-mqtt-influxdb", "mqtt-influxdb", `
		{"mover": "fit", "scenario": "mqtt-influxdb", "variant": "s10-mqtt-influxdb", "records": 100, "counted": 100,
		 "records_per_sec": 900, "cpu_cores": 1, "rss_peak_bytes": 1048576,
		 "lag_ms": {"rate": 10, "p50": 5, "p99": 10, "max": 12}, "rounds": 1},
		{"mover": "telegraf", "scenario": "mqtt-influxdb", "variant": "s10-mqtt-influxdb", "records": 100, "counted": 100,
		 "records_per_sec": 1000, "cpu_cores": 1, "rss_peak_bytes": 2097152,
		 "lag_ms": {"rate": 10, "p50": 4, "p99": 9, "max": 11}, "rounds": 1}`)
	// Files of the load test plan, and of no plan, are left out of a sync
	// plan's scores.
	writeRun(t, dir, "w0-fixed-100.json", &run{Results: []result{{Server: "nginx", Scenario: "fixed", Protocol: "h2c", RequestsPerSec: 1}}})
	if err := os.WriteFile(filepath.Join(dir, "plan-derived.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSyncTables(t *testing.T) {
	dir := syncPlanDir(t)
	text := runMain(t, "-in", filepath.Join(dir, "s1-files-128b-1f.json"))
	for _, want := range []string{"### s1-files-128b-1f, files to a file", "| Mover ", "Records/s [↓1]",
		"| fit      |     2,000 100% |", "5 / 10 / 12 ms", "3 (2.0%)", "3 (10.0%), 1 failed", "0 / 2", "0.1s"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	text = runMain(t, "-in", filepath.Join(dir, "s5-mysql-batch.json"), "-plan", dir)
	for _, want := range []string{"(weight 15.00%)", "| Score |", "| datax |   0.0 |         failed |", "datax failed: the sink had 40"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Lag") {
		t.Errorf("a lag column for a batch:\n%s", text)
	}
}

func TestSyncScores(t *testing.T) {
	dir := syncPlanDir(t)
	p, err := loadPlan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.runs) != 3 || &p.family[0] != &syncWorkloads[0] {
		t.Fatalf("%d runs, family %v", len(p.runs), p.family)
	}
	if w := p.weights[item{"s1-files-128b-1f", "files", "s1-files-128b-1f"}]; math.Abs(w-0.15) > 1e-12 {
		t.Errorf("S1 weight %v", w)
	}
	if w := p.weights[item{"s10-mqtt-influxdb", "mqtt-influxdb", "s10-mqtt-influxdb"}]; math.Abs(w-0.05) > 1e-12 {
		t.Errorf("S10 weight %v", w)
	}
	// The runs in the plan's order: S10 after S5, not after S1.
	if p.runs[len(p.runs)-1].name != "s10-mqtt-influxdb" {
		t.Errorf("the last run %s", p.runs[len(p.runs)-1].name)
	}
	text := runMain(t, "-scores", dir)
	for _, want := range []string{"### Overall ranking", "| 1    | fit ", "|    S1 |", "S5, a MySQL batch copy",
		"records a second", "| filebeat |", "S10, MQTT to InfluxDB", "| telegraf |"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "nginx") || strings.Contains(text, "| W0 ") {
		t.Errorf("the load test plan's results in a sync plan's:\n%s", text)
	}
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "s1-x.json"), []byte(`{"kind": "sync", "results": {}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := load(filepath.Join(bad, "s1-x.json")); err == nil {
		t.Error("a sync file whose results are not a list read")
	}
}

// TestWeightsAddUp checks that each plan's workloads' weights add up to 1.
func TestWeightsAddUp(t *testing.T) {
	for _, family := range [][]workload{workloads, syncWorkloads} {
		sum := 0.0
		for _, w := range family {
			sum += w.weight
		}
		if math.Abs(sum-1) > 1e-9 {
			t.Errorf("%s...: the weights add up to %v", family[0].prefix, sum)
		}
	}
}
