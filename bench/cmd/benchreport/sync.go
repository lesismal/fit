package main

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

// The sync benchmark's results (bench/sync/README.md), as
// bench/sync/syncbench merge writes them: "kind": "sync", and a result for
// each mover, the median of its rounds. load turns each into a result whose
// server is the mover, whose scenario and protocol are the scenario and its
// variant, whose throughput is records a second and whose latency is the
// steady phase's lag, so that the tables are scored as the load test
// plan's are; render draws them as sync's own.

// syncResult is one mover's result.
type syncResult struct {
	Mover          string  `json:"mover"`
	Scenario       string  `json:"scenario"`
	Variant        string  `json:"variant"`
	Records        int64   `json:"records"`
	Counted        int64   `json:"counted"`
	Extra          int64   `json:"extra"`
	RecordsPerSec  float64 `json:"records_per_sec"`
	DrainSeconds   float64 `json:"drain_seconds"`
	CPUCores       float64 `json:"cpu_cores"`
	StartupSeconds float64 `json:"startup_seconds"`
	RSSPeakBytes   int64   `json:"rss_peak_bytes"`
	Lag            *struct {
		Rate int     `json:"rate"`
		P50  float64 `json:"p50"`
		P99  float64 `json:"p99"`
		Max  float64 `json:"max"`
		Mean float64 `json:"mean"`
	} `json:"lag_ms"`
	Error    string   `json:"error"`
	Rounds   int      `json:"rounds"`
	Spread   float64  `json:"spread"`
	Failures []string `json:"failures"`
}

// loadSync fills r's results from a sync results file's data.
func loadSync(r *run, data []byte) error {
	var rep struct {
		Results []syncResult `json:"results"`
	}
	if err := json.Unmarshal(data, &rep); err != nil {
		return err
	}
	r.Results = nil
	for _, s := range rep.Results {
		res := result{Server: s.Mover, Scenario: s.Scenario, Protocol: s.Variant, Error: s.Error,
			RequestsPerSec: s.RecordsPerSec, CPUCores: s.CPUCores, AnonPeakBytes: s.RSSPeakBytes}
		if s.Lag != nil {
			res.Latency = &struct {
				P50  float64 `json:"p50"`
				P99  float64 `json:"p99"`
				P999 float64 `json:"p99.9"`
				Mean float64 `json:"mean"`
				Max  float64 `json:"max"`
			}{P50: s.Lag.P50, P99: s.Lag.P99, Mean: s.Lag.Mean, Max: s.Lag.Max}
		}
		res.sync = &s
		r.Results = append(r.Results, res)
	}
	return nil
}

// scenarioTitles say what each sync scenario moves.
var scenarioTitles = map[string]string{
	"files":       "files to a file",
	"mysql-es":    "MySQL to Elasticsearch",
	"mysql-kafka": "MySQL to Kafka",
	"pg-kafka":    "PostgreSQL to Kafka",
	"mysql-batch": "a MySQL table copied to MySQL",
}

// renderSync renders a sync results file: a table for each scenario and
// variant, a row for each mover, ranked by records a second, then records
// per CPU-second, then records a second per MiB of peak memory.
func renderSync(w io.Writer, cur *run, level int) {
	type key struct{ scenario, variant string }
	var keys []key
	for _, r := range cur.Results {
		if k := (key{r.Scenario, r.Protocol}); !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	hashes := strings.Repeat("#", level)
	for _, k := range keys {
		var rows, failed []result
		for _, r := range cur.Results {
			switch {
			case r.Scenario != k.scenario || r.Protocol != k.variant:
			case r.Error != "":
				failed = append(failed, r)
			default:
				rows = append(rows, r)
			}
		}
		slices.SortStableFunc(rows, func(a, b result) int {
			for _, c := range [][2]float64{{a.tps(), b.tps()}, {a.cpuEER(), b.cpuEER()}, {a.memEER(), b.memEER()}} {
				if c[0] != c[1] {
					if c[0] > c[1] {
						return -1
					}
					return 1
				}
			}
			return 0
		})
		var bestTPS, bestCPU, bestMem float64
		for _, r := range rows {
			bestTPS, bestCPU, bestMem = max(bestTPS, r.tps()), max(bestCPU, r.cpuEER()), max(bestMem, r.memEER())
		}
		lagged := slices.ContainsFunc(rows, func(r result) bool { return r.Latency != nil })
		header := []string{"Mover", "Records/s [↓1]", "Records/CPU-s [↓2]", "Records/s/MiB [↓3]"}
		if lagged {
			header = append(header, "Lag p50 / p99 / max")
		}
		header = append(header, "CPU", "Peak RSS", "Startup", "Rounds (spread)", "Missing / extra")
		var sc map[string]float64
		if scoring != nil {
			sc = tableScores(cur, k.scenario, k.variant)
			header = append(header[:1], append([]string{"Score"}, header[1:]...)...)
		}
		table := [][]string{header}
		for _, r := range rows {
			s := r.sync
			row := []string{r.Server, share(r.tps(), bestTPS, 0), share(r.cpuEER(), bestCPU, 0), share(r.memEER(), bestMem, 1)}
			if lagged {
				lag := "—"
				if r.Latency != nil {
					lag = fmt.Sprintf("%s / %s / %s ms", group(r.Latency.P50, 0), group(r.Latency.P99, 0), group(r.Latency.Max, 0))
				}
				row = append(row, lag)
			}
			rounds := fmt.Sprint(s.Rounds)
			if s.Rounds > 1 {
				rounds += fmt.Sprintf(" (%.1f%%)", s.Spread*100)
			}
			if len(s.Failures) > 0 {
				rounds += fmt.Sprintf(", %d failed", len(s.Failures))
			}
			row = append(row, fmt.Sprintf("%.2f", r.CPUCores), mib(r.memory()), fmt.Sprintf("%.1fs", s.StartupSeconds),
				rounds, fmt.Sprintf("%s / %s", group(float64(s.Records-s.Counted), 0), group(float64(s.Extra), 0)))
			if sc != nil {
				row = append(row[:1], append([]string{fmt.Sprintf("%.1f", sc[r.Server])}, row[1:]...)...)
			}
			table = append(table, row)
		}
		for _, r := range failed {
			row := make([]string, len(header))
			row[0], row[1] = r.Server, "failed"
			if sc != nil {
				row[1], row[2] = "0.0", "failed"
			}
			table = append(table, row)
		}
		title := k.variant
		if t := scenarioTitles[k.scenario]; t != "" && t != k.variant {
			title += ", " + t
		}
		if scoring != nil {
			if weight, ok := scoring.weights[item{cur.name, k.scenario, k.variant}]; ok {
				title += fmt.Sprintf(" (weight %.2f%%)", weight*100)
			}
		}
		fmt.Fprintf(w, "%s %s\n\n", hashes, title)
		writeTable(w, table, 1)
		for _, r := range failed {
			fmt.Fprintf(w, "\n%s failed: %s\n", r.Server, r.Error)
		}
		fmt.Fprintln(w)
	}
}
